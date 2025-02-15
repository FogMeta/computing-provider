package computing

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/filswan/go-mcs-sdk/mcs/api/common/logs"
	"github.com/swanchain/go-computing-provider/conf"
	"github.com/swanchain/go-computing-provider/constants"
	"github.com/swanchain/go-computing-provider/internal/models"
	"github.com/swanchain/go-computing-provider/internal/yaml"
	"github.com/swanchain/go-computing-provider/util"
	appV1 "k8s.io/api/apps/v1"
	coreV1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Deploy struct {
	originalJobUuid   string
	jobUuid           string
	hostName          string
	walletAddress     string
	spaceName         string
	image             string
	dockerfilePath    string
	yamlPath          string
	duration          int64
	hardwareResource  models.Resource
	modelsSettingFile string
	k8sNameSpace      string
	SpacePath         string
	TaskType          string
	hardwareDesc      string
	taskUuid          string
	gpuProductName    string

	// ===
	spaceType   string
	sshKey      string
	nodePortUrl string
	userName    string
	ipWhiteList []string
}

func NewDeploy(originalJobUuid, lowerJobUuid, hostName, walletAddress, hardwareDesc string, duration int64, spaceType string, spaceHardware models.SpaceHardware, jobType int) *Deploy {

	var taskType string
	var hardwareDetail models.Resource

	if jobType == 0 {
		if hardwareDesc != "" {
			taskType, hardwareDetail = getHardwareDetail(hardwareDesc)
		}
	}

	if jobType == 1 {
		taskType, hardwareDetail = getHardwareDetailByByte(spaceHardware)
	}

	return &Deploy{
		originalJobUuid:  originalJobUuid,
		jobUuid:          lowerJobUuid,
		hostName:         hostName,
		walletAddress:    walletAddress,
		duration:         duration,
		hardwareResource: hardwareDetail,
		TaskType:         taskType,
		k8sNameSpace:     constants.K8S_NAMESPACE_NAME_PREFIX + strings.ToLower(walletAddress),
		hardwareDesc:     hardwareDesc,
		spaceType:        spaceType,
	}
}

func (d *Deploy) WithHardware(cpu, memory, storage int, gpuModel string, gpuNum int) *Deploy {
	taskType, hardwareDetail := getHardwareDetailForPrivate(cpu, memory, storage, gpuModel, gpuNum)
	d.hardwareResource = hardwareDetail
	d.TaskType = taskType
	return d
}

func (d *Deploy) WithSshKey(sshKey string) *Deploy {
	d.sshKey = sshKey
	return d
}

func (d *Deploy) WithImage(images string) *Deploy {
	d.image = images
	return d
}

func (d *Deploy) WithSpaceName(spaceName string) *Deploy {
	d.spaceName = spaceName
	return d
}

func (d *Deploy) WithIpWhiteList(ipWhiteList []string) *Deploy {
	d.ipWhiteList = ipWhiteList
	return d
}

func (d *Deploy) WithGpuProductName(gpuProductName string) *Deploy {
	d.gpuProductName = gpuProductName
	return d
}

func (d *Deploy) WithYamlInfo(yamlPath string) *Deploy {
	d.yamlPath = yamlPath
	return d
}

func (d *Deploy) WithDockerfile(image, dockerfilePath string) *Deploy {
	d.image = image
	d.dockerfilePath = dockerfilePath
	return d
}

func (d *Deploy) WithSpacePath(spacePath string) *Deploy {
	d.SpacePath = spacePath
	return d
}

func (d *Deploy) WithModelSettingFile(modelsSettingFile string) *Deploy {
	d.modelsSettingFile = modelsSettingFile
	return d
}

func (d *Deploy) DockerfileToK8s() {
	exposedPort, err := ExtractExposedPort(d.dockerfilePath)
	if err != nil {
		logs.GetLogger().Infof("Failed to extract exposed port: %v", err)
		return
	}
	containerPort, err := strconv.ParseInt(exposedPort, 10, 64)
	if err != nil {
		logs.GetLogger().Errorf("Failed to convert exposed port: %v", err)
		return
	}

	if err := d.deployNamespace(); err != nil {
		logs.GetLogger().Error(err)
		return
	}

	k8sService := NewK8sService()
	deployment := &appV1.Deployment{
		TypeMeta: metaV1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metaV1.ObjectMeta{
			Name:      constants.K8S_DEPLOY_NAME_PREFIX + d.jobUuid,
			Namespace: d.k8sNameSpace,
		},
		Spec: appV1.DeploymentSpec{
			Selector: &metaV1.LabelSelector{
				MatchLabels: map[string]string{"lad_app": d.jobUuid},
			},

			Template: coreV1.PodTemplateSpec{
				ObjectMeta: metaV1.ObjectMeta{
					Labels:    map[string]string{"lad_app": d.jobUuid},
					Namespace: d.k8sNameSpace,
				},

				Spec: coreV1.PodSpec{
					NodeSelector: generateLabel(d.gpuProductName),
					Containers: []coreV1.Container{{
						Name:            constants.K8S_CONTAINER_NAME_PREFIX + d.jobUuid,
						Image:           d.image,
						ImagePullPolicy: coreV1.PullIfNotPresent,
						Ports: []coreV1.ContainerPort{{
							ContainerPort: int32(containerPort),
						}},
						Env:       d.createEnv(),
						Resources: d.createResources(),
					}},
				},
			},
		}}
	_, err = k8sService.CreateDeployment(context.TODO(), d.k8sNameSpace, deployment)
	if err != nil {
		logs.GetLogger().Error(err)
		return
	}
	updateJobStatus(d.originalJobUuid, models.DEPLOY_PULL_IMAGE)

	if _, err := d.deployK8sResource(int32(containerPort)); err != nil {
		logs.GetLogger().Error(err)
		return
	}
	updateJobStatus(d.originalJobUuid, models.DEPLOY_TO_K8S, "https://"+d.hostName)

	d.watchContainerRunningTime()
	return
}

func (d *Deploy) YamlToK8s(nodePort int32) error {
	containerResources, err := yaml.HandlerYaml(d.yamlPath)
	if err != nil {
		logs.GetLogger().Error(err)
		return err
	}

	if err = d.deployNamespace(); err != nil {
		logs.GetLogger().Error(err)
		return err
	}

	if len(containerResources) == 1 && containerResources[0].ServiceType == yaml.ServiceTypeNodePort {
		service := containerResources[0]
		for _, envVar := range service.Env {
			if envVar.Name == "sshkey" {
				d.sshKey = envVar.Value
				d.image = service.ImageName
			}

			if envVar.Name == "username" {
				d.userName = envVar.Value
			}
		}

		if err := d.DeploySshTaskToK8s(service, nodePort); err != nil {
			logs.GetLogger().Error(err)
			return err
		}
		return nil
	}

	k8sService := NewK8sService()
	for _, cr := range containerResources {
		for i, envVar := range cr.Env {
			if strings.Contains(envVar.Name, "NEXTAUTH_URL") {
				cr.Env[i].Value = "https://" + d.hostName
				break
			}
		}

		var volumeMount []coreV1.VolumeMount
		var volumes []coreV1.Volume
		if cr.VolumeMounts.Path != "" {
			fileNameWithoutExt := filepath.Base(cr.VolumeMounts.Name[:len(cr.VolumeMounts.Name)-len(filepath.Ext(cr.VolumeMounts.Name))])
			configMap, err := k8sService.CreateConfigMap(context.TODO(), d.k8sNameSpace, d.jobUuid, filepath.Dir(d.yamlPath), cr.VolumeMounts.Name)
			if err != nil {
				logs.GetLogger().Error(err)
				return err
			}
			configName := configMap.GetName()
			volumes = []coreV1.Volume{
				{
					Name: d.jobUuid + "-" + fileNameWithoutExt,
					VolumeSource: coreV1.VolumeSource{
						ConfigMap: &coreV1.ConfigMapVolumeSource{
							LocalObjectReference: coreV1.LocalObjectReference{
								Name: configName,
							},
						},
					},
				},
			}
			volumeMount = []coreV1.VolumeMount{
				{
					Name:      d.jobUuid + "-" + fileNameWithoutExt,
					MountPath: cr.VolumeMounts.Path,
				},
			}
		}

		var containers []coreV1.Container
		for _, depend := range cr.Depends {
			var ports []coreV1.ContainerPort
			for _, port := range depend.Ports {
				ports = append(ports, coreV1.ContainerPort{
					ContainerPort: port.ContainerPort,
					Protocol:      port.Protocol,
				})
			}

			var handler = new(coreV1.ExecAction)
			handler.Command = depend.ReadyCmd
			containers = append(containers, coreV1.Container{
				Name:            d.jobUuid + "-" + depend.Name,
				Image:           depend.ImageName,
				Command:         depend.Command,
				Args:            depend.Args,
				Env:             depend.Env,
				Ports:           ports,
				ImagePullPolicy: coreV1.PullIfNotPresent,
				Resources:       coreV1.ResourceRequirements{},
				ReadinessProbe: &coreV1.Probe{
					ProbeHandler: coreV1.ProbeHandler{
						Exec: handler,
					},
					InitialDelaySeconds: 5,
					PeriodSeconds:       5,
				},
			})
		}

		cr.Env = append(cr.Env, []coreV1.EnvVar{
			{
				Name:  "wallet_address",
				Value: d.walletAddress,
			},
			{
				Name:  "result_url",
				Value: d.hostName,
			},
			{
				Name:  "job_uuid",
				Value: d.jobUuid,
			},
		}...)

		var ports []coreV1.ContainerPort
		for _, port := range cr.Ports {
			ports = append(ports, coreV1.ContainerPort{
				ContainerPort: port.ContainerPort,
				Protocol:      port.Protocol,
			})
		}

		containers = append(containers, coreV1.Container{
			Name:            d.jobUuid + "-" + cr.Name,
			Image:           cr.ImageName,
			Command:         cr.Command,
			Args:            cr.Args,
			Env:             cr.Env,
			Ports:           ports,
			ImagePullPolicy: coreV1.PullIfNotPresent,
			Resources:       d.createResources(),
			VolumeMounts:    volumeMount,
		})

		deployment := &appV1.Deployment{
			TypeMeta: metaV1.TypeMeta{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metaV1.ObjectMeta{
				Name:      constants.K8S_DEPLOY_NAME_PREFIX + d.jobUuid,
				Namespace: d.k8sNameSpace,
			},

			Spec: appV1.DeploymentSpec{
				Selector: &metaV1.LabelSelector{
					MatchLabels: map[string]string{"lad_app": d.jobUuid},
				},
				Template: coreV1.PodTemplateSpec{
					ObjectMeta: metaV1.ObjectMeta{
						Labels:    map[string]string{"lad_app": d.jobUuid},
						Namespace: d.k8sNameSpace,
					},
					Spec: coreV1.PodSpec{
						NodeSelector: generateLabel(d.gpuProductName),
						Containers:   containers,
						Volumes:      volumes,
					},
				},
			}}

		if _, err = k8sService.CreateDeployment(context.TODO(), d.k8sNameSpace, deployment); err != nil {
			logs.GetLogger().Error(err)
			return err
		}
		updateJobStatus(d.originalJobUuid, models.DEPLOY_PULL_IMAGE)

		serviceHost, err := d.deployK8sResource(cr.Ports[0].ContainerPort)
		if err != nil {
			logs.GetLogger().Error(err)
			return err
		}

		updateJobStatus(d.originalJobUuid, models.DEPLOY_TO_K8S, "https://"+d.hostName)

		if len(cr.Models) > 0 {
			for _, res := range cr.Models {
				go func(res yaml.ModelResource) {
					downloadModelUrl(d.k8sNameSpace, d.jobUuid, serviceHost, []string{"wget", res.Url, "-O", filepath.Join(res.Dir, res.Name)})
				}(res)
			}
		}
		d.watchContainerRunningTime()
	}
	return nil
}

func (d *Deploy) ModelInferenceToK8s() error {
	var modelSetting struct {
		ModelId string `json:"model_id"`
	}
	modelData, _ := os.ReadFile(d.modelsSettingFile)
	err := json.Unmarshal(modelData, &modelSetting)
	if err != nil {
		logs.GetLogger().Errorf("convert model_id out to json failed, error: %+v", err)
		return err
	}

	cpPath, _ := os.LookupEnv("CP_PATH")
	basePath := filepath.Join(cpPath, "inference-model")

	modelInfoOut, err := util.RunPythonScript(filepath.Join(basePath, "/scripts/hf_client.py"), "model_info", modelSetting.ModelId)
	if err != nil {
		logs.GetLogger().Errorf("exec model_info cmd failed, error: %+v", err)
		return err
	}

	var modelInfo struct {
		ModelId   string `json:"model_id"`
		Task      string `json:"task"`
		Framework string `json:"framework"`
	}
	err = json.Unmarshal([]byte(modelInfoOut), &modelInfo)
	if err != nil {
		logs.GetLogger().Errorf("convert model_info out to json failed, error: %+v", err)
		return err
	}

	imageName := "lagrange/" + modelInfo.Framework + ":v1.0"

	logFile := filepath.Join(d.SpacePath, BuildFileName)
	if _, err = os.Create(logFile); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)
	util.StreamPythonScriptOutput(&wg, filepath.Join(basePath, "build_docker.py"), basePath, modelInfo.Framework, imageName, logFile)
	wg.Wait()

	modelEnvs := []coreV1.EnvVar{
		{
			Name:  "TASK",
			Value: modelInfo.Task,
		},
		{
			Name:  "MODEL_ID",
			Value: modelInfo.ModelId,
		},
	}

	d.image = imageName

	if err := d.deployNamespace(); err != nil {
		logs.GetLogger().Error(err)
		return err
	}

	k8sService := NewK8sService()
	deployment := &appV1.Deployment{
		TypeMeta: metaV1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metaV1.ObjectMeta{
			Name:      constants.K8S_DEPLOY_NAME_PREFIX + d.jobUuid,
			Namespace: d.k8sNameSpace,
		},
		Spec: appV1.DeploymentSpec{
			Selector: &metaV1.LabelSelector{
				MatchLabels: map[string]string{"lad_app": d.jobUuid},
			},

			Template: coreV1.PodTemplateSpec{
				ObjectMeta: metaV1.ObjectMeta{
					Labels:    map[string]string{"lad_app": d.jobUuid},
					Namespace: d.k8sNameSpace,
				},

				Spec: coreV1.PodSpec{
					NodeSelector: generateLabel(d.gpuProductName),
					Containers: []coreV1.Container{{
						Name:            constants.K8S_CONTAINER_NAME_PREFIX + d.jobUuid,
						Image:           d.image,
						ImagePullPolicy: coreV1.PullIfNotPresent,
						Ports: []coreV1.ContainerPort{{
							ContainerPort: int32(80),
						}},
						Env: d.createEnv(modelEnvs...),
						//Resources: d.createResources(),
					}},
				},
			},
		}}
	if _, err = k8sService.CreateDeployment(context.TODO(), d.k8sNameSpace, deployment); err != nil {
		logs.GetLogger().Error(err)
		return err
	}
	updateJobStatus(d.originalJobUuid, models.DEPLOY_PULL_IMAGE)

	if _, err = d.deployK8sResource(int32(80)); err != nil {
		logs.GetLogger().Error(err)
		return err
	}
	updateJobStatus(d.originalJobUuid, models.DEPLOY_TO_K8S)
	d.watchContainerRunningTime()
	return nil
}

func (d *Deploy) DeploySshTaskToK8s(containerResource yaml.ContainerResource, nodePort int32) error {
	k8sService := NewK8sService()
	volumeMounts, volumes := generateVolume()

	var exclude22Port []int32
	for _, port := range containerResource.Ports {
		if port.ContainerPort != 22 {
			exclude22Port = append(exclude22Port, port.ContainerPort)
		}
	}

	deployment := &appV1.Deployment{
		TypeMeta: metaV1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metaV1.ObjectMeta{
			Name:      constants.K8S_DEPLOY_NAME_PREFIX + d.jobUuid,
			Namespace: d.k8sNameSpace,
			Annotations: map[string]string{
				"initializer.kubernetes.io/lxcfs": "true",
			},
		},
		Spec: appV1.DeploymentSpec{
			Selector: &metaV1.LabelSelector{
				MatchLabels: map[string]string{"hub-private": d.jobUuid},
			},

			Template: coreV1.PodTemplateSpec{
				ObjectMeta: metaV1.ObjectMeta{
					Labels:    map[string]string{"hub-private": d.jobUuid},
					Namespace: d.k8sNameSpace,
				},

				Spec: coreV1.PodSpec{
					Hostname:     d.spaceName + "-" + generateString(4),
					NodeSelector: generateLabel(d.gpuProductName),
					Containers: []coreV1.Container{
						{
							Name:            constants.K8S_PRIVATE_CONTAINER_PREFIX + d.jobUuid,
							Image:           d.image,
							ImagePullPolicy: coreV1.PullIfNotPresent,
							Ports:           containerResource.Ports,
							Resources:       d.createResources(),
							VolumeMounts:    volumeMounts,
						},
					},
					Volumes: volumes,
				},
			},
		}}
	if _, err := k8sService.CreateDeployment(context.TODO(), d.k8sNameSpace, deployment); err != nil {
		return fmt.Errorf("failed to create deployment, job_uuid: %s error: %v", d.jobUuid, err)
	}
	updateJobStatus(d.originalJobUuid, models.DEPLOY_PULL_IMAGE)

	podName, err := k8sService.WaitForPodRunningByTcp(d.k8sNameSpace, d.jobUuid)
	if err != nil {
		return fmt.Errorf("job_uuid: %s, %v", d.jobUuid, err)
	}

	sshkeyCmd := []string{"sh", "-c", fmt.Sprintf("echo '%s' > /root/.ssh/authorized_keys", d.sshKey)}
	if err = k8sService.PodDoCommand(d.k8sNameSpace, podName, "", sshkeyCmd); err != nil {
		return fmt.Errorf("failed to add sshkey, job_uuid: %s error: %v", d.jobUuid, err)
	}

	randomPassword := generateRandomPassword(8)
	usernameAndPwdCmd := []string{"sh", "-c", fmt.Sprintf("useradd %s && echo \"%s:%s\" | chpasswd", d.userName, d.userName, randomPassword)}
	if err = k8sService.PodDoCommand(d.k8sNameSpace, podName, "", usernameAndPwdCmd); err != nil {
		return fmt.Errorf("failed to add user, job_uuid: %s error: %v", d.jobUuid, err)
	}

	userDir := fmt.Sprintf("/home/%s", d.userName)
	userDirCmd := []string{"sh", "-c", fmt.Sprintf("mkdir -p %s && chown %s:%s %s && chmod 755 %s", userDir, d.userName, d.userName, userDir, userDir)}
	if err = k8sService.PodDoCommand(d.k8sNameSpace, podName, "", userDirCmd); err != nil {
		return fmt.Errorf("failed to create user directory, job_uuid: %s error: %v", d.jobUuid, err)
	}

	createService, err := k8sService.CreateServiceByNodePort(context.TODO(), d.k8sNameSpace, d.jobUuid, 22, nodePort, exclude22Port)
	if err != nil {
		return fmt.Errorf("failed to create service, job_uuid: %s error: %v", d.jobUuid, err)
	}

	var portMap string
	for _, port := range createService.Spec.Ports {
		portMap += fmt.Sprintf("%s:%d, ", port.TargetPort.String(), port.NodePort)
	}
	d.nodePortUrl = fmt.Sprintf("ssh root@%s -p%d、username: %s,password: %s; %s",
		strings.Split(conf.GetConfig().API.MultiAddress, "/")[2], nodePort, d.userName, randomPassword, portMap)
	d.watchContainerRunningTime()
	return nil
}

func (d *Deploy) deployNamespace() error {
	k8sService := NewK8sService()
	if _, err := k8sService.GetNameSpace(context.TODO(), d.k8sNameSpace, metaV1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			namespace := &coreV1.Namespace{
				ObjectMeta: metaV1.ObjectMeta{
					Name: d.k8sNameSpace,
					Labels: map[string]string{
						"lab-ns": strings.ToLower(d.walletAddress),
					},
				},
			}
			_, err = k8sService.CreateNameSpace(context.TODO(), namespace, metaV1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed create namespace, error: %w", err)
			}

			//networkPolicy, err := k8sService.CreateNetworkPolicy(context.TODO(), k8sNameSpace)
			//if err != nil {
			//	return fmt.Errorf("failed create networkPolicy, error: %w", err)
			//}
			//logs.GetLogger().Infof("create networkPolicy successfully, networkPolicyName: %s", networkPolicy.Name)
		} else {
			return err
		}
	}
	return nil
}

func (d *Deploy) createEnv(envs ...coreV1.EnvVar) []coreV1.EnvVar {
	defaultEnv := []coreV1.EnvVar{
		{
			Name:  "space_name",
			Value: d.spaceName,
		},
		{
			Name:  "result_url",
			Value: d.hostName,
		},
		{
			Name:  "job_uuid",
			Value: d.jobUuid,
		},
	}

	defaultEnv = append(defaultEnv, envs...)
	return defaultEnv
}

func (d *Deploy) createResources() coreV1.ResourceRequirements {

	memQuantity, err := resource.ParseQuantity(fmt.Sprintf("%d%s", d.hardwareResource.Memory.Quantity, d.hardwareResource.Memory.Unit))
	if err != nil {
		logs.GetLogger().Error("get memory failed, error: %+v", err)
		return coreV1.ResourceRequirements{}
	}

	storageQuantity, err := resource.ParseQuantity(fmt.Sprintf("%d%s", d.hardwareResource.Storage.Quantity, d.hardwareResource.Storage.Unit))
	if err != nil {
		logs.GetLogger().Error("get storage failed, error: %+v", err)
		return coreV1.ResourceRequirements{}
	}

	return coreV1.ResourceRequirements{
		Limits: coreV1.ResourceList{
			coreV1.ResourceCPU:              *resource.NewQuantity(d.hardwareResource.Cpu.Quantity, resource.DecimalSI),
			coreV1.ResourceMemory:           memQuantity,
			coreV1.ResourceEphemeralStorage: storageQuantity,
			"nvidia.com/gpu":                resource.MustParse(fmt.Sprintf("%d", d.hardwareResource.Gpu.Quantity)),
		},
		Requests: coreV1.ResourceList{
			coreV1.ResourceCPU:              *resource.NewQuantity(d.hardwareResource.Cpu.Quantity, resource.DecimalSI),
			coreV1.ResourceMemory:           memQuantity,
			coreV1.ResourceEphemeralStorage: storageQuantity,
			"nvidia.com/gpu":                resource.MustParse(fmt.Sprintf("%d", d.hardwareResource.Gpu.Quantity)),
		},
	}
}

func (d *Deploy) deployK8sResource(containerPort int32) (string, error) {
	k8sService := NewK8sService()

	createService, err := k8sService.CreateService(context.TODO(), d.k8sNameSpace, d.jobUuid, containerPort)
	if err != nil {
		return "", fmt.Errorf("failed to create service, error: %w", err)
	}

	serviceHost := fmt.Sprintf("http://%s:%d", createService.Spec.ClusterIP, createService.Spec.Ports[0].Port)

	_, err = k8sService.CreateIngress(context.TODO(), d.k8sNameSpace, d.jobUuid, d.hostName, containerPort, d.ipWhiteList)
	if err != nil {
		return "", fmt.Errorf("failed to create ingress, error: %w", err)
	}
	return serviceHost, nil
}

func (d *Deploy) watchContainerRunningTime() {
	var job = new(models.JobEntity)
	job.JobUuid = d.jobUuid
	job.ExpireTime = time.Now().Unix() + d.duration
	job.ImageName = d.image
	job.K8sResourceType = "deployment"
	if err := NewJobService().UpdateJobEntityByJobUuid(job); err != nil {
		logs.GetLogger().Errorf("failed to update job info, error: %v", err)
		return
	}
	logs.GetLogger().Infof("space service deployed, job_uuid: %s, spaceName: %s", d.jobUuid, d.spaceName)
}

func generateRandomPassword(length int) string {
	charset := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	seededRand := rand.New(rand.NewSource(time.Now().UnixNano()))
	password := make([]byte, length)
	for i := range password {
		password[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(password)
}

func getHardwareDetail(description string) (string, models.Resource) {
	var taskType string
	var hardwareResource models.Resource
	confSplits := strings.Split(description, "·")
	if strings.Contains(confSplits[0], "CPU") {
		hardwareResource.Gpu.Quantity = 0
		hardwareResource.Gpu.Unit = ""
		taskType = "CPU"
		hardwareResource.Storage.Quantity = 5
	} else {
		taskType = "GPU"
		hardwareResource.Gpu.Quantity = 1
		oldName := strings.TrimSpace(confSplits[0])
		hardwareResource.Gpu.Unit = strings.ReplaceAll(oldName, "Nvidia", "NVIDIA")

		hardwareResource.Storage.Quantity = 50
	}
	hardwareResource.Storage.Unit = "Gi"

	cpuSplits := strings.Split(confSplits[1], " ")
	cores, _ := strconv.ParseInt(cpuSplits[1], 10, 64)
	hardwareResource.Cpu.Quantity = cores
	hardwareResource.Cpu.Unit = cpuSplits[2]

	memSplits := strings.Split(confSplits[2], " ")
	mem, _ := strconv.ParseInt(memSplits[1], 10, 64)
	hardwareResource.Memory.Quantity = mem
	hardwareResource.Memory.Unit = strings.ReplaceAll(memSplits[2], "B", "")

	return taskType, hardwareResource
}

func getHardwareDetailByByte(spaceHardware models.SpaceHardware) (string, models.Resource) {
	var hardwareResource models.Resource

	hardwareResource.Cpu.Unit = "vCPU"
	hardwareResource.Cpu.Quantity = spaceHardware.Vcpu
	hardwareResource.Memory.Unit = "Gi"
	hardwareResource.Memory.Quantity = spaceHardware.Memory
	hardwareResource.Storage.Unit = "Gi"
	hardwareResource.Storage.Quantity = spaceHardware.Storage

	if spaceHardware.Storage == 0 {
		hardwareResource.Storage.Quantity = 5
	}

	if strings.Contains(spaceHardware.HardwareType, "GPU") {
		hardwareResource.Gpu.Quantity = 1
		if spaceHardware.Gpu != 0 {
			hardwareResource.Gpu.Quantity = spaceHardware.Gpu
		}
		hardwareResource.Gpu.Unit = strings.ReplaceAll(spaceHardware.Hardware, "Nvidia", "NVIDIA")
		if spaceHardware.Storage == 0 {
			hardwareResource.Storage.Quantity = 50
		}
	}
	return spaceHardware.HardwareType, hardwareResource
}

func getHardwareDetailForPrivate(cpu, memory, storage int, gpuModel string, gpuNum int) (string, models.Resource) {
	var taskType string
	var hardwareResource models.Resource

	hardwareResource.Cpu.Quantity = int64(cpu)
	hardwareResource.Cpu.Unit = "vCPU"

	hardwareResource.Memory.Quantity = int64(memory)
	hardwareResource.Memory.Unit = "Gi"

	hardwareResource.Storage.Quantity = int64(storage)
	hardwareResource.Storage.Unit = "Gi"

	hardwareResource.Gpu.Quantity = int64(gpuNum)
	hardwareResource.Gpu.Unit = strings.ReplaceAll(gpuModel, "Nvidia", "NVIDIA")
	if len(strings.TrimSpace(gpuModel)) == 0 {
		taskType = "CPU"
	} else {
		taskType = "GPU"
	}

	return taskType, hardwareResource
}
