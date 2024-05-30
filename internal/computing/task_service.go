package computing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/filswan/go-mcs-sdk/mcs/api/common/logs"
	"github.com/gomodule/redigo/redis"
	"github.com/swanchain/go-computing-provider/conf"
	"github.com/swanchain/go-computing-provider/constants"
	models2 "github.com/swanchain/go-computing-provider/internal/models"
	"io"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var deployingChan = make(chan models2.Job)

type ScheduleTask struct {
	TaskMap sync.Map
}

func NewScheduleTask() *ScheduleTask {
	return &ScheduleTask{}
}

func (s *ScheduleTask) Run() {
	for {
		select {
		case job := <-deployingChan:
			s.TaskMap.Store(job.Uuid, &job)
		case <-time.After(3 * time.Second):
			s.TaskMap.Range(func(key, value any) bool {
				jobUuid := key.(string)
				job := value.(*models2.Job)
				if reportJobStatus(jobUuid, job.Status) && job.Status == models2.JobDeployToK8s {
					s.TaskMap.Delete(jobUuid)
				}
				return true
			})
		}
	}
}

func reportJobStatus(jobUuid string, jobStatus models2.JobStatus) bool {
	reqParam := map[string]interface{}{
		"job_uuid":       jobUuid,
		"status":         jobStatus,
		"public_address": conf.GetConfig().HUB.WalletAddress,
	}

	payload, err := json.Marshal(reqParam)
	if err != nil {
		logs.GetLogger().Errorf("Failed convert to json, error: %+v", err)
		return false
	}

	client := &http.Client{}
	url := conf.GetConfig().HUB.ServerUrl + "/job/status"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		logs.GetLogger().Errorf("Error creating request: %v", err)
		return false
	}
	req.Header.Set("Authorization", "Bearer "+conf.GetConfig().HUB.AccessToken)
	req.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		logs.GetLogger().Errorf("Failed send a request, error: %+v", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	logs.GetLogger().Debugf("report job status successfully. uuid: %s, status: %s", jobUuid, jobStatus)
	return true
}

func RunSyncTask(nodeId string) {
	go func() {
		k8sService := NewK8sService()
		nodes, err := k8sService.k8sClient.CoreV1().Nodes().List(context.TODO(), metaV1.ListOptions{})
		if err != nil {
			logs.GetLogger().Error(err)
			return
		}

		nodeGpuInfoMap, err := k8sService.GetResourceExporterPodLog(context.TODO())
		if err != nil {
			logs.GetLogger().Error(err)
			return
		}

		logs.GetLogger().Infof("collect all node: %d", len(nodes.Items))
		for _, node := range nodes.Items {
			cpNode := node
			if collectInfo, ok := nodeGpuInfoMap[cpNode.Name]; ok {
				for _, detail := range collectInfo.Gpu.Details {
					if err = k8sService.AddNodeLabel(cpNode.Name, detail.ProductName); err != nil {
						logs.GetLogger().Errorf("add node label, nodeName %s, gpuName: %s, error: %+v", cpNode.Name, detail.ProductName, err)
						continue
					}
				}
				k8sService.AddNodeLabel(cpNode.Name, collectInfo.CpuName)
			}
		}
	}()

	go func() {
		defer func() {
			if err := recover(); err != nil {
				logs.GetLogger().Errorf("Failed report cp resource's summary, error: %+v", err)
			}
		}()

		location, err := getLocation()
		if err != nil {
			logs.GetLogger().Error(err)
		}

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			reportClusterResource(location, nodeId)
			checkClusterProviderStatus()
		}

	}()

	watchExpiredTask()
	watchNameSpaceForDeleted()
	monitorDaemonSetPods()
}

func reportClusterResource(location, nodeId string) {
	k8sService := NewK8sService()
	statisticalSources, err := k8sService.StatisticalSources(context.TODO())
	if err != nil {
		logs.GetLogger().Errorf("Failed k8s statistical sources, error: %+v", err)
		return
	}
	clusterSource := models2.ClusterResource{
		NodeId:        nodeId,
		Region:        location,
		ClusterInfo:   statisticalSources,
		PublicAddress: conf.GetConfig().HUB.WalletAddress,
	}

	payload, err := json.Marshal(clusterSource)
	if err != nil {
		logs.GetLogger().Errorf("Failed convert to json, error: %+v", err)
		return
	}

	client := &http.Client{}
	url := conf.GetConfig().HUB.ServerUrl + "/cp/summary"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		logs.GetLogger().Errorf("Error creating request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+conf.GetConfig().HUB.AccessToken)
	req.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		logs.GetLogger().Errorf("Failed send a request, error: %+v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logs.GetLogger().Errorf("report cluster node resources failed, status code: %d", resp.StatusCode)
		return
	}

}

func watchExpiredTask() {
	cpRepoPath, _ := os.LookupEnv("CP_PATH")
	nodeId := GetNodeId(cpRepoPath)
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		var deleteKey []string
		for range ticker.C {
			go func() {
				defer func() {
					if err := recover(); err != nil {
						logs.GetLogger().Errorf("watchExpiredTask catch panic error: %+v", err)
					}
				}()
				conn := redisPool.Get()
				prefix := constants.REDIS_SPACE_PREFIX + "*"
				keys, err := redis.Strings(conn.Do("KEYS", prefix))
				if err != nil {
					logs.GetLogger().Errorf("Failed get redis %s prefix, error: %+v", prefix, err)
					return
				}
				for _, key := range keys {
					jobMetadata, err := RetrieveJobMetadata(key)
					if err != nil {
						logs.GetLogger().Errorf("Failed get redis key data, key: %s, error: %+v", key, err)
						return
					}

					namespace := constants.K8S_NAMESPACE_NAME_PREFIX + strings.ToLower(jobMetadata.WalletAddress)

					if len(strings.TrimSpace(jobMetadata.TaskUuid)) != 0 {
						taskStatus, err := checkTaskStatusByHub(jobMetadata.TaskUuid, nodeId)
						if err != nil {
							logs.GetLogger().Errorf("Failed check task status by Orchestrator service, error: %+v", err)
							return
						}
						if strings.Contains(taskStatus, "Task not found") {
							logs.GetLogger().Infof("task_uuid: %s, task not found on the orchestrator service, starting to delete it.", jobMetadata.TaskUuid)
							deleteJob(namespace, jobMetadata.SpaceUuid)
							deleteKey = append(deleteKey, key)
							continue
						}
						if strings.Contains(taskStatus, "Terminated") || strings.Contains(taskStatus, "Terminated") ||
							strings.Contains(taskStatus, "Cancelled") || strings.Contains(taskStatus, "Failed") {
							logs.GetLogger().Infof("task_uuid: %s, current status is %s, starting to delete it.", jobMetadata.TaskUuid, taskStatus)
							if err = deleteJob(namespace, jobMetadata.SpaceUuid); err == nil {
								deleteKey = append(deleteKey, key)
								continue
							}
						}
					}

					if time.Now().Unix() > jobMetadata.ExpireTime {
						expireTimeStr := time.Unix(jobMetadata.ExpireTime, 0).Format("2006-01-02 15:04:05")
						logs.GetLogger().Infof("<timer-task> redis-key: %s,expireTime: %s. the job starting terminated", key, expireTimeStr)
						if err = deleteJob(namespace, jobMetadata.SpaceUuid); err == nil {
							deleteKey = append(deleteKey, key)
							continue
						}
					}

					k8sNameSpace := constants.K8S_NAMESPACE_NAME_PREFIX + strings.ToLower(jobMetadata.WalletAddress)
					deployName := constants.K8S_DEPLOY_NAME_PREFIX + jobMetadata.SpaceUuid
					service := NewK8sService()
					if _, err = service.k8sClient.AppsV1().Deployments(k8sNameSpace).Get(context.TODO(), deployName, metaV1.GetOptions{}); err != nil && errors.IsNotFound(err) {
						deleteKey = append(deleteKey, key)
						continue
					}
				}
				conn.Do("DEL", redis.Args{}.AddFlat(deleteKey)...)
				if len(deleteKey) > 0 {
					logs.GetLogger().Infof("Delete redis keys finished, keys: %+v", deleteKey)
					deleteKey = nil
				}
			}()
		}
	}()
}

func watchNameSpaceForDeleted() {
	ticker := time.NewTicker(50 * time.Minute)
	go func() {
		for range ticker.C {
			go func() {
				defer func() {
					if err := recover(); err != nil {
						logs.GetLogger().Errorf("watchNameSpaceForDeleted catch panic error: %+v", err)
					}
				}()
				service := NewK8sService()
				namespaces, err := service.ListNamespace(context.TODO())
				if err != nil {
					logs.GetLogger().Errorf("Failed get all namespace, error: %+v", err)
					return
				}

				for _, namespace := range namespaces {
					getPods, err := service.GetPods(namespace, "")
					if err != nil {
						logs.GetLogger().Errorf("Failed get pods form namespace,namepace: %s, error: %+v", namespace, err)
						continue
					}
					if !getPods && (strings.HasPrefix(namespace, constants.K8S_NAMESPACE_NAME_PREFIX) || strings.HasPrefix(namespace, "ubi-task")) {
						if err = service.DeleteNameSpace(context.TODO(), namespace); err != nil {
							logs.GetLogger().Errorf("Failed delete namespace, namepace: %s, error: %+v", namespace, err)
						}
					}
				}
				NewDockerService().CleanResource()
			}()
		}
	}()
}

func checkTaskStatusByHub(taskUuid, nodeId string) (string, error) {
	url := fmt.Sprintf("%s/check_task_status_with_node_id/%s/%s", conf.GetConfig().HUB.ServerUrl, taskUuid, nodeId)
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Add("offset", "0")
	req.Header.Add("limit", "10")
	req.Header.Add("Authorization", "Bearer "+conf.GetConfig().HUB.AccessToken)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error making HTTP request:", err)
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var taskStatus struct {
		Data struct {
			JobStatus  string `json:"job_status"`
			TaskStatus string `json:"task_status"`
		} `json:"data"`
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	err = json.Unmarshal(respBody, &taskStatus)
	if err != nil {
		logs.GetLogger().Errorf("check_task_status_with_node_id resp: %s", string(respBody))
		return "", err
	}
	if taskStatus.Status == "failed" {
		return taskStatus.Message, nil
	}
	return taskStatus.Status, nil
}

func monitorDaemonSetPods() {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				logs.GetLogger().Errorf("monitorDaemonSetPods catch panic error: %+v", err)
			}
		}()

		namespace := "kube-system"
		service := NewK8sService()
		stopCh := wait.NeverStop
		var num int64 = 1
		podLogOptions := corev1.PodLogOptions{
			Container:  "",
			TailLines:  &num,
			Timestamps: false,
		}

		var errorCount = make(map[string]int)
		wait.Until(func() {
			pods, err := service.k8sClient.CoreV1().Pods(namespace).List(context.TODO(), metaV1.ListOptions{
				LabelSelector: "app=resource-exporter",
			})
			if err != nil {
				logs.GetLogger().Errorf("get resource-exporter pods failed, error: %+v", err)
				return
			}

			for _, pod := range pods.Items {
				if pod.Status.Phase != corev1.PodRunning {
					service.k8sClient.CoreV1().Pods(namespace).Delete(context.TODO(), pod.Name, metaV1.DeleteOptions{})
					continue
				}
				podLog, err := service.GetPodLogByPodName(namespace, pod.Name, &podLogOptions)
				if err != nil {
					logs.GetLogger().Errorf("collect gpu deatil info, podName: %s, error: %+v", pod.Name, err)
					continue
				}
				if strings.Contains(podLog, "ERROR::") {
					if errorCount[pod.Name] > 2 {
						service.k8sClient.CoreV1().Pods(namespace).Delete(context.TODO(), pod.Name, metaV1.DeleteOptions{})
						delete(errorCount, pod.Name)
						continue
					}
					errorCount[pod.Name]++
				}
			}
		}, 2*time.Minute, stopCh)
	}()

}
