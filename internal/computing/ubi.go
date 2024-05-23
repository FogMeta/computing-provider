package computing

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/filswan/go-swan-lib/logs"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/swanchain/go-computing-provider/account"
	"github.com/swanchain/go-computing-provider/build"
	"github.com/swanchain/go-computing-provider/conf"
	"github.com/swanchain/go-computing-provider/constants"
	"github.com/swanchain/go-computing-provider/internal/models"
	"github.com/swanchain/go-computing-provider/util"
	"github.com/swanchain/go-computing-provider/wallet"
	"io"
	batchv1 "k8s.io/api/batch/v1"
	coreV1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func DoUbiTaskForK8s(c *gin.Context) {

	var ubiTask models.UBITaskReq
	if err := c.ShouldBindJSON(&ubiTask); err != nil {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.JsonError))
		return
	}
	logs.GetLogger().Infof("receive ubi task received: %+v", ubiTask)

	if ubiTask.ID == 0 {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: id"))
		return
	}
	if strings.TrimSpace(ubiTask.Name) == "" {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: name"))
		return
	}

	if ubiTask.ResourceType != 0 && ubiTask.ResourceType != 1 {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "the value of resource_type is 0 or 1"))
		return
	}
	if strings.TrimSpace(ubiTask.ZkType) == "" {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: zk_type"))
		return
	}

	if strings.TrimSpace(ubiTask.InputParam) == "" {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: input_param"))
		return
	}

	if strings.TrimSpace(ubiTask.Signature) == "" {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: signature"))
		return
	}
	if strings.TrimSpace(ubiTask.ContractAddr) == "" {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: contract_addr"))
		return
	}

	cpRepoPath, _ := os.LookupEnv("CP_PATH")
	nodeID := GetNodeId(cpRepoPath)

	signature, err := verifySignature(conf.GetConfig().UBI.UbiEnginePk, fmt.Sprintf("%s%s", nodeID, ubiTask.ContractAddr), ubiTask.Signature)
	if err != nil {
		logs.GetLogger().Errorf("verifySignature for ubi task failed, error: %+v", err)
		c.JSON(http.StatusInternalServerError, util.CreateErrorResponse(util.UbiTaskParamError, "sign data failed"))
		return
	}

	logs.GetLogger().Infof("ubi task sign verifing, task_id: %d, type: %s, verify: %v", ubiTask.ID, ubiTask.ZkType, signature)
	if !signature {
		c.JSON(http.StatusInternalServerError, util.CreateErrorResponse(util.UbiTaskParamError, "signature verify failed"))
		return
	}

	var gpuFlag = "0"
	if ubiTask.ResourceType == 1 {
		gpuFlag = "1"
	}

	var taskEntity = new(models.TaskEntity)
	taskEntity.Id = int64(ubiTask.ID)
	taskEntity.ZkType = ubiTask.ZkType
	taskEntity.Name = ubiTask.Name
	taskEntity.Contract = ubiTask.ContractAddr
	taskEntity.ResourceType = ubiTask.ResourceType
	taskEntity.InputParam = ubiTask.InputParam
	taskEntity.Status = models.TASK_RECEIVED_STATUS
	taskEntity.CreateTime = time.Now().Unix()
	err = NewTaskService().SaveTaskEntity(taskEntity)
	if err != nil {
		logs.GetLogger().Errorf("save task entity failed, error: %v", err)
		return
	}

	var envFilePath string
	envFilePath = filepath.Join(os.Getenv("CP_PATH"), "fil-c2.env")
	envVars, err := godotenv.Read(envFilePath)
	if err != nil {
		logs.GetLogger().Errorf("reading fil-c2-env.env failed, error: %v", err)
		return
	}

	c2GpuConfig := envVars["RUST_GPU_TOOLS_CUSTOM_GPU"]
	c2GpuName := convertGpuName(strings.TrimSpace(c2GpuConfig))
	nodeName, architecture, needCpu, needMemory, needStorage, err := checkResourceAvailableForUbi(ubiTask.ResourceType, c2GpuName, ubiTask.Resource)
	if err != nil {
		taskEntity.Status = models.TASK_FAILED_STATUS
		NewTaskService().SaveTaskEntity(taskEntity)
		logs.GetLogger().Errorf("check resource failed, error: %v", err)
		c.JSON(http.StatusInternalServerError, util.CreateErrorResponse(util.CheckResourcesError))
		return
	}

	if nodeName == "" {
		taskEntity.Status = models.TASK_FAILED_STATUS
		NewTaskService().SaveTaskEntity(taskEntity)
		logs.GetLogger().Warnf("ubi task id: %d, type: %s, not found a resources available", ubiTask.ID, models.GetSourceTypeStr(ubiTask.ResourceType))
		c.JSON(http.StatusInternalServerError, util.CreateErrorResponse(util.CheckAvailableResources))
		return
	}

	var ubiTaskImage string
	if architecture == constants.CPU_AMD {
		ubiTaskImage = build.UBITaskImageAmdCpu
		if gpuFlag == "1" {
			ubiTaskImage = build.UBITaskImageAmdGpu
		}
	} else if architecture == constants.CPU_INTEL {
		ubiTaskImage = build.UBITaskImageIntelCpu
		if gpuFlag == "1" {
			ubiTaskImage = build.UBITaskImageIntelGpu
		}
	}

	mem := strings.Split(strings.TrimSpace(ubiTask.Resource.Memory), " ")[1]
	memUnit := strings.ReplaceAll(mem, "B", "")
	disk := strings.Split(strings.TrimSpace(ubiTask.Resource.Storage), " ")[1]
	diskUnit := strings.ReplaceAll(disk, "B", "")
	memQuantity, err := resource.ParseQuantity(fmt.Sprintf("%d%s", needMemory, memUnit))
	if err != nil {
		taskEntity.Status = models.TASK_FAILED_STATUS
		NewTaskService().SaveTaskEntity(taskEntity)
		logs.GetLogger().Error("get memory failed, error: %+v", err)
		return
	}

	storageQuantity, err := resource.ParseQuantity(fmt.Sprintf("%d%s", needStorage, diskUnit))
	if err != nil {
		taskEntity.Status = models.TASK_FAILED_STATUS
		NewTaskService().SaveTaskEntity(taskEntity)
		logs.GetLogger().Error("get storage failed, error: %+v", err)
		return
	}

	maxMemQuantity, err := resource.ParseQuantity(fmt.Sprintf("%d%s", needMemory*2, memUnit))
	if err != nil {
		taskEntity.Status = models.TASK_FAILED_STATUS
		NewTaskService().SaveTaskEntity(taskEntity)
		logs.GetLogger().Error("get memory failed, error: %+v", err)
		return
	}

	maxStorageQuantity, err := resource.ParseQuantity(fmt.Sprintf("%d%s", needStorage*2, diskUnit))
	if err != nil {
		taskEntity.Status = models.TASK_FAILED_STATUS
		NewTaskService().SaveTaskEntity(taskEntity)
		logs.GetLogger().Error("get storage failed, error: %+v", err)
		return
	}

	resourceRequirements := coreV1.ResourceRequirements{
		Limits: coreV1.ResourceList{
			coreV1.ResourceCPU:              *resource.NewQuantity(needCpu*2, resource.DecimalSI),
			coreV1.ResourceMemory:           maxMemQuantity,
			coreV1.ResourceEphemeralStorage: maxStorageQuantity,
			"nvidia.com/gpu":                resource.MustParse(gpuFlag),
		},
		Requests: coreV1.ResourceList{
			coreV1.ResourceCPU:              *resource.NewQuantity(needCpu, resource.DecimalSI),
			coreV1.ResourceMemory:           memQuantity,
			coreV1.ResourceEphemeralStorage: storageQuantity,
			"nvidia.com/gpu":                resource.MustParse(gpuFlag),
		},
	}

	go func() {
		var namespace = "ubi-task-" + strconv.Itoa(ubiTask.ID)
		var err error
		defer func() {
			ubiTaskRun, err := NewTaskService().GetTaskEntity(int64(ubiTask.ID))
			if err != nil {
				logs.GetLogger().Errorf("get ubi task detail from db failed, ubiTaskId: %d, error: %+v", ubiTask.ID, err)
				return
			}
			if ubiTaskRun.Id == 0 {
				ubiTaskRun = new(models.TaskEntity)
				ubiTaskRun.Id = int64(ubiTask.ID)
				ubiTaskRun.ZkType = ubiTask.ZkType
				ubiTaskRun.Name = ubiTask.Name
				ubiTaskRun.Contract = ubiTask.ContractAddr
				ubiTaskRun.ResourceType = ubiTask.ResourceType
				ubiTaskRun.InputParam = ubiTask.InputParam
				ubiTaskRun.CreateTime = time.Now().Unix()
				ubiTaskRun.Contract = ubiTask.ContractAddr
			}

			if err == nil {
				ubiTaskRun.Status = models.TASK_RUNNING_STATUS
			} else {
				ubiTaskRun.Status = models.TASK_FAILED_STATUS
				k8sService := NewK8sService()
				k8sService.k8sClient.CoreV1().Namespaces().Delete(context.TODO(), namespace, metaV1.DeleteOptions{})
			}
			err = NewTaskService().SaveTaskEntity(ubiTaskRun)
		}()

		k8sService := NewK8sService()
		if _, err = k8sService.GetNameSpace(context.TODO(), namespace, metaV1.GetOptions{}); err != nil {
			if errors.IsNotFound(err) {
				k8sNamespace := &v1.Namespace{
					ObjectMeta: metaV1.ObjectMeta{
						Name: namespace,
					},
				}
				_, err = k8sService.CreateNameSpace(context.TODO(), k8sNamespace, metaV1.CreateOptions{})
				if err != nil {
					logs.GetLogger().Errorf("create namespace failed, error: %v", err)
					return
				}
			}
		}

		receiveUrl := fmt.Sprintf("%s:%d/api/v1/computing/cp/receive/ubi", k8sService.GetAPIServerEndpoint(), conf.GetConfig().API.Port)
		execCommand := []string{"ubi-bench", "c2"}
		JobName := strings.ToLower(ubiTask.ZkType) + "-" + strconv.Itoa(ubiTask.ID)
		filC2Param := envVars["FIL_PROOFS_PARAMETER_CACHE"]
		if gpuFlag == "0" {
			delete(envVars, "RUST_GPU_TOOLS_CUSTOM_GPU")
			envVars["BELLMAN_NO_GPU"] = "1"
		}

		delete(envVars, "FIL_PROOFS_PARAMETER_CACHE")
		var useEnvVars []v1.EnvVar
		for k, v := range envVars {
			useEnvVars = append(useEnvVars, v1.EnvVar{
				Name:  k,
				Value: v,
			})
		}

		useEnvVars = append(useEnvVars, v1.EnvVar{
			Name:  "RECEIVE_PROOF_URL",
			Value: receiveUrl,
		},
			v1.EnvVar{
				Name:  "TASKID",
				Value: strconv.Itoa(ubiTask.ID),
			},
			v1.EnvVar{
				Name:  "NAME_SPACE",
				Value: namespace,
			},
			v1.EnvVar{
				Name:  "PARAM_URL",
				Value: ubiTask.InputParam,
			},
		)

		job := &batchv1.Job{
			ObjectMeta: metaV1.ObjectMeta{
				Name:      JobName,
				Namespace: namespace,
			},
			Spec: batchv1.JobSpec{
				Template: v1.PodTemplateSpec{
					Spec: v1.PodSpec{
						NodeName:     nodeName,
						NodeSelector: generateLabel(strings.ReplaceAll(c2GpuName, " ", "-")),
						Containers: []v1.Container{
							{
								Name:  JobName + generateString(5),
								Image: ubiTaskImage,
								Env:   useEnvVars,
								VolumeMounts: []v1.VolumeMount{
									{
										Name:      "proof-params",
										MountPath: "/var/tmp/filecoin-proof-parameters",
									},
								},
								Command:         execCommand,
								Resources:       resourceRequirements,
								ImagePullPolicy: coreV1.PullIfNotPresent,
							},
						},
						Volumes: []v1.Volume{
							{
								Name: "proof-params",
								VolumeSource: v1.VolumeSource{
									HostPath: &v1.HostPathVolumeSource{
										Path: filC2Param,
									},
								},
							},
						},
						RestartPolicy: "Never",
					},
				},
				BackoffLimit:            new(int32),
				TTLSecondsAfterFinished: new(int32),
			},
		}

		*job.Spec.BackoffLimit = 1
		*job.Spec.TTLSecondsAfterFinished = 120

		if _, err = k8sService.k8sClient.BatchV1().Jobs(namespace).Create(context.TODO(), job, metaV1.CreateOptions{}); err != nil {
			logs.GetLogger().Errorf("Failed creating ubi task job: %v", err)
			return
		}

		time.Sleep(4 * time.Second)

		pods, err := k8sService.k8sClient.CoreV1().Pods(namespace).List(context.TODO(), metaV1.ListOptions{
			LabelSelector: fmt.Sprintf("job-name=%s", JobName),
		})

		var podName string
		for _, pod := range pods.Items {
			podName = pod.Name
			break
		}

		req := k8sService.k8sClient.CoreV1().Pods(namespace).GetLogs(podName, &v1.PodLogOptions{
			Container:  "",
			Follow:     true,
			Timestamps: true,
		})

		time.Sleep(2 * time.Second)
		podLogs, err := req.Stream(context.Background())
		if err != nil {
			logs.GetLogger().Errorf("Error opening log stream: %v", err)
			return
		}
		defer podLogs.Close()

		ubiLogFileName := filepath.Join(cpRepoPath, "ubi.log")
		logFile, err := os.OpenFile(ubiLogFileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			logs.GetLogger().Errorf("opening ubi log file failed, error: %v", err)
			return
		}
		defer logFile.Close()

		if _, err = io.Copy(logFile, podLogs); err != nil {
			logs.GetLogger().Errorf("write ubi log to file failed, error: %v", err)
			return
		}
	}()

	c.JSON(http.StatusOK, util.CreateSuccessResponse("success"))
}

func ReceiveUbiProofForK8s(c *gin.Context) {
	var c2Proof struct {
		TaskId    string `json:"task_id"`
		Proof     string `json:"proof"`
		NameSpace string `json:"name_space"`
	}

	if err := c.ShouldBindJSON(&c2Proof); err != nil {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.JsonError))
		return
	}
	logs.GetLogger().Infof("task_id: %s, C2 proof out received: %+v", c2Proof.TaskId, c2Proof)

	taskId, err := strconv.Atoi(c2Proof.TaskId)
	if err != nil {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.JsonError))
		return
	}

	var submitUBIProofTx string
	ubiTask, err := NewTaskService().GetTaskEntity(int64(taskId))
	if err != nil {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.JsonError))
		return
	}

	defer func() {
		if err == nil {
			ubiTask.Status = models.TASK_SUCCESS_STATUS
		} else {
			ubiTask.Status = models.TASK_FAILED_STATUS
		}
		ubiTask.TxHash = submitUBIProofTx
		NewTaskService().SaveTaskEntity(ubiTask)

		if strings.TrimSpace(c2Proof.NameSpace) != "" {
			k8sService := NewK8sService()
			k8sService.k8sClient.CoreV1().Namespaces().Delete(context.TODO(), c2Proof.NameSpace, metaV1.DeleteOptions{})
		}
	}()

	chainUrl, err := conf.GetRpcByName(conf.DefaultRpc)
	if err != nil {
		logs.GetLogger().Errorf("get rpc url failed, error: %v,", err)
		return
	}

	client, err := ethclient.Dial(chainUrl)
	if err != nil {
		logs.GetLogger().Errorf("dial rpc connect failed, error: %v,", err)
		return
	}
	defer client.Close()

	cpStub, err := account.NewAccountStub(client)
	if err != nil {
		logs.GetLogger().Errorf("create ubi task client failed, error: %v,", err)
		return
	}
	cpAccount, err := cpStub.GetCpAccountInfo()
	if err != nil {
		logs.GetLogger().Errorf("get account info failed, error: %v,", err)
		return
	}

	localWallet, err := wallet.SetupWallet(wallet.WalletRepo)
	if err != nil {
		logs.GetLogger().Errorf("setup wallet failed, error: %v,", err)
		return
	}

	_, workerAddress, err := GetOwnerAddressAndWorkerAddress()
	if err != nil {
		logs.GetLogger().Errorf("get worker address failed, error: %v", err)
		return
	}

	ki, err := localWallet.FindKey(workerAddress)
	if err != nil || ki == nil {
		logs.GetLogger().Errorf("the address: %s, private key %v,", cpAccount.OwnerAddress, wallet.ErrKeyInfoNotFound)
		return
	}

	taskStub, err := account.NewTaskStub(client, account.WithTaskContractAddress(ubiTask.Contract), account.WithTaskPrivateKey(ki.PrivateKey))
	if err != nil {
		logs.GetLogger().Errorf("create ubi task client failed, error: %v,", err)
		return
	}

	submitUBIProofTx, err = taskStub.SubmitUBIProof(c2Proof.Proof)
	if err != nil {
		logs.GetLogger().Errorf("submit ubi proof tx failed, error: %v,", err)
		return
	}

	logs.GetLogger().Infof("submitUBIProofTx: %s", submitUBIProofTx)
	c.JSON(http.StatusOK, util.CreateSuccessResponse("success"))
}

func DoUbiTaskForDocker(c *gin.Context) {

	var ubiTask models.UBITaskReq
	if err := c.ShouldBindJSON(&ubiTask); err != nil {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.JsonError))
		return
	}

	logs.GetLogger().Infof("ubi task received: id: %d, type: %d, zk_type: %s, input_param: %s, signature: %s, contract: %s",
		ubiTask.ID, ubiTask.ResourceType, ubiTask.ZkType, ubiTask.InputParam, ubiTask.Signature, ubiTask.ContractAddr)

	if ubiTask.ID == 0 {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: id"))
		return
	}
	if strings.TrimSpace(ubiTask.Name) == "" {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: name"))
		return
	}

	if ubiTask.ResourceType != 0 && ubiTask.ResourceType != 1 {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "the value of resource_type is 0 or 1"))
		return
	}
	if strings.TrimSpace(ubiTask.ZkType) == "" {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: zk_type"))
		return
	}

	if strings.TrimSpace(ubiTask.InputParam) == "" {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: input_param"))
		return
	}

	if strings.TrimSpace(ubiTask.Signature) == "" {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: signature"))
		return
	}
	if strings.TrimSpace(ubiTask.ContractAddr) == "" {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.UbiTaskParamError, "missing required field: contract_addr"))
		return
	}

	cpRepoPath, _ := os.LookupEnv("CP_PATH")
	nodeID := GetNodeId(cpRepoPath)

	signature, err := verifySignature(conf.GetConfig().UBI.UbiEnginePk, fmt.Sprintf("%s%s", nodeID, ubiTask.ContractAddr), ubiTask.Signature)
	if err != nil {
		logs.GetLogger().Errorf("verifySignature for ubi task failed, error: %+v", err)
		c.JSON(http.StatusInternalServerError, util.CreateErrorResponse(util.UbiTaskParamError, "sign data failed"))
		return
	}

	logs.GetLogger().Infof("ubi task sign verifing, task_id: %d, verify: %v", ubiTask.ID, signature)
	if !signature {
		c.JSON(http.StatusInternalServerError, util.CreateErrorResponse(util.UbiTaskParamError, "signature verify failed"))
		return
	}

	var gpuFlag = "0"
	if ubiTask.ResourceType == 1 {
		gpuFlag = "1"
	}

	var taskEntity = new(models.TaskEntity)
	taskEntity.Id = int64(ubiTask.ID)
	taskEntity.ZkType = ubiTask.ZkType
	taskEntity.Name = ubiTask.Name
	taskEntity.Contract = ubiTask.ContractAddr
	taskEntity.ResourceType = ubiTask.ResourceType
	taskEntity.InputParam = ubiTask.InputParam
	taskEntity.Status = models.TASK_RECEIVED_STATUS
	taskEntity.CreateTime = time.Now().Unix()
	err = NewTaskService().SaveTaskEntity(taskEntity)
	if err != nil {
		logs.GetLogger().Errorf("save task entity failed, error: %v", err)
		return
	}

	var gpuName string
	gpuConfig, ok := os.LookupEnv("RUST_GPU_TOOLS_CUSTOM_GPU")
	if ok {
		gpuName = convertGpuName(strings.TrimSpace(gpuConfig))
	}

	suffice, architecture, _, needMemory, err := checkResourceForUbi(ubiTask.Resource, gpuName, ubiTask.ResourceType)
	if err != nil {
		taskEntity.Status = models.TASK_FAILED_STATUS
		NewTaskService().SaveTaskEntity(taskEntity)
		logs.GetLogger().Errorf("check resource failed, error: %v", err)
		c.JSON(http.StatusInternalServerError, util.CreateErrorResponse(util.CheckResourcesError))
		return
	}

	if !suffice {
		taskEntity.Status = models.TASK_FAILED_STATUS
		NewTaskService().SaveTaskEntity(taskEntity)
		logs.GetLogger().Warnf("ubi task id: %d, type: %s, not found a resources available", ubiTask.ID, models.GetSourceTypeStr(ubiTask.ResourceType))
		c.JSON(http.StatusInternalServerError, util.CreateErrorResponse(util.CheckAvailableResources))
		return
	}

	var ubiTaskImage string
	if architecture == constants.CPU_AMD {
		ubiTaskImage = build.UBITaskImageAmdCpu
		if gpuFlag == "1" {
			ubiTaskImage = build.UBITaskImageAmdGpu
		}
	} else if architecture == constants.CPU_INTEL {
		ubiTaskImage = build.UBITaskImageIntelCpu
		if gpuFlag == "1" {
			ubiTaskImage = build.UBITaskImageIntelGpu
		}
	}

	if err := NewDockerService().PullImage(ubiTaskImage); err != nil {
		logs.GetLogger().Errorf("pull %s image failed, error: %v", ubiTaskImage, err)
		return
	}

	go func() {
		defer func() {
			ubiTaskRun, err := NewTaskService().GetTaskEntity(int64(ubiTask.ID))
			if err != nil {
				logs.GetLogger().Errorf("get ubi task detail from db failed, ubiTaskId: %d, error: %+v", ubiTask.ID, err)
				return
			}
			if ubiTaskRun.Id == 0 {
				ubiTaskRun = new(models.TaskEntity)
				ubiTaskRun.Id = int64(ubiTask.ID)
				ubiTaskRun.ZkType = ubiTask.ZkType
				ubiTaskRun.Name = ubiTask.Name
				ubiTaskRun.Contract = ubiTask.ContractAddr
				ubiTaskRun.ResourceType = ubiTask.ResourceType
				ubiTaskRun.InputParam = ubiTask.InputParam
				ubiTaskRun.CreateTime = time.Now().Unix()
				ubiTaskRun.Contract = ubiTask.ContractAddr
			}
			if err == nil {
				ubiTaskRun.Status = models.TASK_RUNNING_STATUS
			} else {
				ubiTaskRun.Status = models.TASK_FAILED_STATUS
			}
			NewTaskService().SaveTaskEntity(ubiTaskRun)
		}()

		multiAddressSplit := strings.Split(conf.GetConfig().API.MultiAddress, "/")
		receiveUrl := fmt.Sprintf("http://%s:%s/api/v1/computing/cp/docker/receive/ubi", multiAddressSplit[2], multiAddressSplit[4])
		execCommand := []string{"ubi-bench", "c2"}
		JobName := strings.ToLower(ubiTask.ZkType) + "-" + strconv.Itoa(ubiTask.ID)

		var env = []string{"RECEIVE_PROOF_URL=" + receiveUrl}
		env = append(env, "TASKID="+strconv.Itoa(ubiTask.ID))
		env = append(env, "PARAM_URL="+ubiTask.InputParam)

		var needResource container.Resources
		if gpuFlag == "0" {
			env = append(env, "BELLMAN_NO_GPU=1")
			needResource = container.Resources{
				Memory: needMemory * 1024 * 1024 * 1024,
			}
		} else {
			gpuEnv, ok := os.LookupEnv("RUST_GPU_TOOLS_CUSTOM_GPU")
			if ok {
				env = append(env, "RUST_GPU_TOOLS_CUSTOM_GPU="+gpuEnv)
			}
			needResource = container.Resources{
				Memory: needMemory * 1024 * 1024 * 1024,
				DeviceRequests: []container.DeviceRequest{
					{
						Driver:       "nvidia",
						Count:        -1,
						Capabilities: [][]string{{"gpu"}},
						Options:      nil,
					},
				},
			}
		}

		filC2Param, ok := os.LookupEnv("FIL_PROOFS_PARAMETER_CACHE")
		if !ok {
			filC2Param = "/var/tmp/filecoin-proof-parameters"
		}

		hostConfig := &container.HostConfig{
			Binds:     []string{fmt.Sprintf("%s:/var/tmp/filecoin-proof-parameters", filC2Param)},
			Resources: needResource,
		}
		containerConfig := &container.Config{
			Image:        ubiTaskImage,
			Cmd:          execCommand,
			Env:          env,
			AttachStdout: true,
			AttachStderr: true,
			Tty:          true,
		}

		dockerService := NewDockerService()
		if err = dockerService.ContainerCreateAndStart(containerConfig, hostConfig, JobName+generateString(5)); err != nil {
			logs.GetLogger().Errorf("create ubi task container failed, error: %v", err)
		}
	}()
	c.JSON(http.StatusOK, util.CreateSuccessResponse("success"))
}

func checkResourceForUbi(resource *models.TaskResource, gpuName string, resourceType int) (bool, string, int64, int64, error) {
	dockerService := NewDockerService()
	containerLogStr, err := dockerService.ContainerLogs("resource-exporter")
	if err != nil {
		return false, "", 0, 0, err
	}

	var nodeResource models.NodeResource
	if err := json.Unmarshal([]byte(containerLogStr), &nodeResource); err != nil {
		logs.GetLogger().Error("collect host hardware resource failed, error: %+v", err)
		return false, "", 0, 0, err
	}

	needCpu, _ := strconv.ParseInt(resource.CPU, 10, 64)
	var needMemory, needStorage float64
	if len(strings.Split(strings.TrimSpace(resource.Memory), " ")) > 0 {
		needMemory, err = strconv.ParseFloat(strings.Split(strings.TrimSpace(resource.Memory), " ")[0], 64)

	}
	if len(strings.Split(strings.TrimSpace(resource.Storage), " ")) > 0 {
		needStorage, err = strconv.ParseFloat(strings.Split(strings.TrimSpace(resource.Storage), " ")[0], 64)
	}

	remainderCpu, _ := strconv.ParseInt(nodeResource.Cpu.Free, 10, 64)

	var remainderMemory, remainderStorage float64
	if len(strings.Split(strings.TrimSpace(nodeResource.Memory.Free), " ")) > 0 {
		remainderMemory, _ = strconv.ParseFloat(strings.Split(strings.TrimSpace(nodeResource.Memory.Free), " ")[0], 64)
	}
	if len(strings.Split(strings.TrimSpace(nodeResource.Storage.Free), " ")) > 0 {
		remainderStorage, err = strconv.ParseFloat(strings.Split(strings.TrimSpace(nodeResource.Storage.Free), " ")[0], 64)
	}

	var gpuMap = make(map[string]int)
	if nodeResource.Gpu.AttachedGpus > 0 {
		for _, detail := range nodeResource.Gpu.Details {
			if detail.Status == models.Available {
				gpuMap[detail.ProductName] += 1
			}
		}
	}

	logs.GetLogger().Infof("checkResourceForUbi: needCpu: %d, needMemory: %.2f, needStorage: %.2f", needCpu, needMemory, needStorage)
	logs.GetLogger().Infof("checkResourceForUbi: remainingCpu: %d, remainingMemory: %.2f, remainingStorage: %.2f, remainingGpu: %+v", remainderCpu, remainderMemory, remainderStorage, gpuMap)
	if needCpu <= remainderCpu && needMemory <= remainderMemory && needStorage <= remainderStorage {
		if resourceType == 1 {
			if gpuName != "" {
				var flag bool
				for k, num := range gpuMap {
					if strings.ToUpper(k) == gpuName && num > 0 {
						flag = true
						break
					}
				}
				if flag {
					return true, nodeResource.CpuName, needCpu, int64(needMemory), nil
				} else {
					return false, nodeResource.CpuName, needCpu, int64(needMemory), nil
				}
			}
		}
		return true, nodeResource.CpuName, needCpu, int64(needMemory), nil
	}
	return false, nodeResource.CpuName, needCpu, int64(needMemory), nil
}

func ReceiveUbiProofForDocker(c *gin.Context) {
	var submitUBIProofTx string
	var err error
	var c2Proof models.UbiC2Proof

	if err := c.ShouldBindJSON(&c2Proof); err != nil {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.JsonError))
		return
	}
	logs.GetLogger().Infof("task_id: %s, c2 proof out received: %+v", c2Proof.TaskId, c2Proof)

	taskId, err := strconv.Atoi(c2Proof.TaskId)
	if err != nil {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.JsonError))
		return
	}
	ubiTask, err := NewTaskService().GetTaskEntity(int64(taskId))
	if err != nil {
		c.JSON(http.StatusBadRequest, util.CreateErrorResponse(util.JsonError))
		return
	}

	defer func() {
		if err == nil {
			ubiTask.Status = models.TASK_SUCCESS_STATUS
		} else {
			ubiTask.Status = models.TASK_FAILED_STATUS
		}
		ubiTask.TxHash = submitUBIProofTx
		NewTaskService().SaveTaskEntity(ubiTask)
	}()

	retries := 3
	for i := 0; i < retries; i++ {
		submitUBIProofTx, err = submitUBIProof(c2Proof, ubiTask.Contract)
		if err == nil {
			break
		}
		time.Sleep(time.Second * 2)
	}
	logs.GetLogger().Infof("submitUBIProofTx: %s", submitUBIProofTx)
	c.JSON(http.StatusOK, util.CreateSuccessResponse("success"))
}

func GetCpResource(c *gin.Context) {
	location, err := getLocation()
	if err != nil {
		logs.GetLogger().Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed get location info"})
		return
	}

	dockerService := NewDockerService()
	containerLogStr, err := dockerService.ContainerLogs("resource-exporter")
	if err != nil {
		logs.GetLogger().Error("collect host hardware resource failed, error: %+v", err)
		return
	}

	var nodeResource models.NodeResource
	if err := json.Unmarshal([]byte(containerLogStr), &nodeResource); err != nil {
		logs.GetLogger().Error("hardware info parse to json failed, error: %+v", err)
		return
	}

	cpRepo, _ := os.LookupEnv("CP_PATH")
	c.JSON(http.StatusOK, models.ClusterResource{
		Region:       location,
		ClusterInfo:  []*models.NodeResource{&nodeResource},
		MultiAddress: conf.GetConfig().API.MultiAddress,
		NodeName:     conf.GetConfig().API.NodeName,
		NodeId:       GetNodeId(cpRepo),
	})
}

func submitUBIProof(c2Proof models.UbiC2Proof, contractAddress string) (string, error) {
	chainUrl, err := conf.GetRpcByName(conf.DefaultRpc)
	if err != nil {
		logs.GetLogger().Errorf("get rpc url failed, error: %v,", err)
		return "", err
	}
	client, err := ethclient.Dial(chainUrl)
	if err != nil {
		logs.GetLogger().Errorf("dial rpc connect failed, error: %v,", err)
		return "", err
	}
	client.Close()

	cpStub, err := account.NewAccountStub(client)
	if err != nil {
		logs.GetLogger().Errorf("create ubi task client failed, error: %v,", err)
		return "", err
	}
	cpAccount, err := cpStub.GetCpAccountInfo()
	if err != nil {
		logs.GetLogger().Errorf("get account info failed, error: %v,", err)
		return "", err
	}

	localWallet, err := wallet.SetupWallet(wallet.WalletRepo)
	if err != nil {
		logs.GetLogger().Errorf("setup wallet failed, error: %v,", err)
		return "", err
	}

	_, workerAddress, err := GetOwnerAddressAndWorkerAddress()
	if err != nil {
		logs.GetLogger().Errorf("get worker address failed, error: %v", err)
		return "", err
	}

	ki, err := localWallet.FindKey(workerAddress)
	if err != nil || ki == nil {
		logs.GetLogger().Errorf("the address: %s, private key %v,", cpAccount.OwnerAddress, wallet.ErrKeyInfoNotFound)
		return "", err
	}

	taskStub, err := account.NewTaskStub(client, account.WithTaskContractAddress(contractAddress), account.WithTaskPrivateKey(ki.PrivateKey))
	if err != nil {
		logs.GetLogger().Errorf("create ubi task client failed, error: %v,", err)
		return "", err
	}

	submitUBIProofTx, err := taskStub.SubmitUBIProof(c2Proof.Proof)
	if err != nil {
		logs.GetLogger().Errorf("submit ubi proof tx failed, error: %v,", err)
		return "", err
	}
	return submitUBIProofTx, nil
}

func reportClusterResourceForDocker() {
	dockerService := NewDockerService()
	containerLogStr, err := dockerService.ContainerLogs("resource-exporter")
	if err != nil {
		logs.GetLogger().Errorf("collect host hardware resource failed, error: %v", err)
		resourceExporterContainerName := "resource-exporter"
		err = dockerService.RemoveImageByName(resourceExporterContainerName)
		if err != nil {
			logs.GetLogger().Errorf("remove %s container failed, error: %v", resourceExporterContainerName, err)
			return
		}

		dockerService.PullImage(build.UBIResourceExporterDockerImage)
		if err = dockerService.ContainerCreateAndStart(&container.Config{
			Image:        build.UBIResourceExporterDockerImage,
			AttachStdout: true,
			AttachStderr: true,
			Tty:          true,
		}, nil, resourceExporterContainerName); err != nil {
			logs.GetLogger().Errorf("create resource-exporter container failed, error: %v", err)
		}
		return
	}

	var nodeResource models.NodeResource
	if err := json.Unmarshal([]byte(containerLogStr), &nodeResource); err != nil {
		logs.GetLogger().Warnf("hardware info parse to json failed, restarting resource-exporter")
		resourceExporterContainerName := "resource-exporter"
		err = NewDockerService().RemoveImageByName(resourceExporterContainerName)
		if err != nil {
			logs.GetLogger().Errorf("remove %s container failed, error: %v", resourceExporterContainerName, err)
			return
		}
		dockerService.PullImage(build.UBIResourceExporterDockerImage)
		if err = dockerService.ContainerCreateAndStart(&container.Config{
			Image:        build.UBIResourceExporterDockerImage,
			AttachStdout: true,
			AttachStderr: true,
			Tty:          true,
		}, nil, resourceExporterContainerName); err != nil {
			logs.GetLogger().Errorf("create resource-exporter container failed, error: %v", err)
		}
		return
	}

	var freeGpuMap = make(map[string]int)
	if nodeResource.Gpu.AttachedGpus > 0 {
		for _, g := range nodeResource.Gpu.Details {
			if g.Status == models.Available {
				freeGpuMap[g.ProductName] += 1
			} else {
				freeGpuMap[g.ProductName] = 0
			}
		}
	}
	logs.GetLogger().Infof("collect hardware resource, freeCpu:%s, freeMemory: %s, freeStorage: %s, freeGpu: %v",
		nodeResource.Cpu.Free, nodeResource.Memory.Free, nodeResource.Storage.Free, freeGpuMap)
}

func CleanDockerResource() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			NewDockerService().CleanResource()
		}
	}()

	go func() {
		ticker := time.NewTicker(3 * time.Minute)
		for range ticker.C {
			reportClusterResourceForDocker()
		}
	}()

	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			var taskList []models.TaskEntity
			oneHourAgo := time.Now().Add(-1 * time.Hour).Unix()
			err := NewTaskService().Model(&models.TaskEntity{}).Where("status !=? and status !=? and create_time <?", models.TASK_SUCCESS_STATUS, models.TASK_FAILED_STATUS, oneHourAgo).Or("tx_hash==''").Find(&taskList).Error
			if err != nil {
				logs.GetLogger().Errorf("Failed get task list, error: %+v", err)
				return
			}

			for _, entity := range taskList {
				ubiTask := entity
				ubiTask.Status = models.TASK_FAILED_STATUS
				NewTaskService().SaveTaskEntity(&ubiTask)
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(3 * time.Minute)
		for range ticker.C {
			taskList, err := NewTaskService().GetTaskListNoReward()
			if err != nil {
				logs.GetLogger().Errorf("Failed get task list, error: %+v", err)
				return
			}

			for _, entity := range taskList {
				ubiTask := entity
				err = getReward(ubiTask)
				if err != nil {
					logs.GetLogger().Errorf("get ubi task reward failed, taskId: %d, error: %v", ubiTask.Id, err)
					continue
				}
			}
		}
	}()
}

func getReward(task *models.TaskEntity) error {
	chainUrl, err := conf.GetRpcByName(conf.DefaultRpc)
	if err != nil {
		logs.GetLogger().Errorf("get rpc url failed, error: %v,", err)
		return err
	}

	client, err := ethclient.Dial(chainUrl)
	if err != nil {
		logs.GetLogger().Errorf("dial rpc connect failed, error: %v,", err)
		return err
	}
	defer client.Close()

	taskStub, err := account.NewTaskStub(client, account.WithTaskContractAddress(task.Contract))
	if err != nil {
		logs.GetLogger().Errorf("create ubi task client failed, error: %v,", err)
		return err
	}

	status, rewardTx, challengeTx, slashTx, reward, err := taskStub.GetReward()
	if err != nil {
		return err
	}
	if status != models.REWARD_UNCLAIMED {
		task.Reward = reward
		task.RewardStatus = status
		task.RewardTx = rewardTx
		task.ChallengeTx = challengeTx
		task.SlashTx = slashTx
		return NewTaskService().SaveTaskEntity(task)
	}

	return nil
}
