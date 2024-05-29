package models

import (
	"encoding/json"
	"gorm.io/gorm"
)

const (
	SOURCE_TYPE_CPU = 0
	SOURCE_TYPE_GPU = 1
)

const (
	ZK_TYPE_FIL_32  = "fil-c2-32G"
	ZK_TYPE_FIL_512 = "fil-c2-512M"
	ZK_TYPE_ALEO    = "aleo_proof"
)

const (
	TASK_RECEIVED_STATUS = iota + 1
	TASK_RUNNING_STATUS
	TASK_SUCCESS_STATUS
	TASK_FAILED_STATUS
)

func TaskStatusStr(status int) string {
	var statusStr string
	switch status {
	case TASK_RECEIVED_STATUS:
		statusStr = "received"
	case TASK_RUNNING_STATUS:
		statusStr = "running"
	case TASK_SUCCESS_STATUS:
		statusStr = "success"
	case TASK_FAILED_STATUS:
		statusStr = "failed"
	}
	return statusStr
}

func GetSourceTypeStr(resourceType int) string {
	switch resourceType {
	case SOURCE_TYPE_CPU:
		return "CPU"
	case SOURCE_TYPE_GPU:
		return "GPU"
	}
	return ""
}

const (
	REWARD_UNCLAIMED = iota
	REWARD_CHALLENGED
	REWARD_SLASHED
	REWARD_CLAIMED
)

func GetRewardStr(status int) string {
	var statusStr string
	switch status {
	case REWARD_UNCLAIMED:
		statusStr = "unclaimed"
	case REWARD_CHALLENGED:
		statusStr = "challenged"
	case REWARD_SLASHED:
		statusStr = "slashed"
	case REWARD_CLAIMED:
		statusStr = "claimed"
	}
	return statusStr
}

type TaskEntity struct {
	Id           int64  `json:"id" gorm:"primaryKey;id"`
	ZkType       string `json:"zk_type" gorm:"zk_type"`
	Name         string `json:"name" gorm:"name"`
	Contract     string `json:"contract" gorm:"name"`
	ResourceType int    `json:"resource_type" gorm:"resource_type"` // 1
	InputParam   string `json:"input_param" gorm:"input_param"`
	TxHash       string `json:"tx_hash" gorm:"tx_hash"`
	RewardTx     string `json:"reward_tx"`
	ChallengeTx  string `json:"challenge_tx"`
	SlashTx      string `json:"slash_tx"`
	Status       int    `json:"status" gorm:"status"`
	RewardStatus int    `json:"reward_status" gorm:"status"` // 0: unclaimed; 1: challenged; 2: slashed; 3: claimed
	Reward       string `json:"reward" gorm:"column:reward; default:0"`
	CreateTime   int64  `json:"create_time" gorm:"create_time"`
	EndTime      int64  `json:"end_time" gorm:"end_time"`
	Error        string `json:"error" gorm:"error"`
}

func (task *TaskEntity) TableName() string {
	return "t_task"
}

const (
	DEPLOY_RECEIVE_JOB = iota + 1
	DEPLOY_DOWNLOAD_SOURCE
	DEPLOY_UPLOAD_RESULT
	DEPLOY_BUILD_IMAGE
	DEPLOY_PUSH_IMAGE
	DEPLOY_PULL_IMAGE
	DEPLOY_TO_K8S
)

func GetDeployStatusStr(deployStatus int) string {
	var statusStr string
	switch deployStatus {
	case DEPLOY_DOWNLOAD_SOURCE:
		statusStr = "downloadSource"
	case DEPLOY_UPLOAD_RESULT:
		statusStr = "uploadResult"
	case DEPLOY_BUILD_IMAGE:
		statusStr = "buildImage"
	case DEPLOY_PUSH_IMAGE:
		statusStr = "pushImage"
	case DEPLOY_PULL_IMAGE:
		statusStr = "pullImage"
	case DEPLOY_TO_K8S:
		statusStr = "deployToK8s"
	}
	return statusStr
}

type JobEntity struct {
	Id              int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	Source          string `json:"source" gorm:"source"` // market name
	Name            string `json:"name" gorm:"name"`
	SpaceUuid       string `json:"space_uuid"`
	JobUuid         string `json:"job_uuid"`
	TaskUuid        string `json:"task_uuid"`
	ResourceType    string `json:"resource_type"`
	SpaceType       int    `json:"space_type"` // 0: public; 1: private
	SourceUrl       string `json:"source_url" gorm:"source_url"`
	Hardware        string `json:"hardware" gorm:"hardware"`
	Duration        int    `json:"duration"`
	DeployStatus    int    `json:"deploy_status" gorm:"deploy_status"`
	WalletAddress   string `json:"wallet_address"`
	ResultUrl       string `json:"result_url" gorm:"result_url"`
	RealUrl         string `json:"real_url"`
	K8sDeployName   string `json:"k8s_deploy_name" gorm:"k8s_deploy_name"`
	K8sResourceType string `json:"k8s_resource_type" gorm:"k8s_resource_type"`
	NameSpace       string `json:"name_space" gorm:"name_space"`
	ImageName       string `json:"image_name" gorm:"image_name"`
	BuildLog        string `json:"build_log" gorm:"build_log"`
	ContainerLog    string `json:"container_log" gorm:"container_log"`
	ExpireTime      int64  `json:"expire_time" gorm:"expire_time"`
	CreateTime      int64  `json:"create_time" gorm:"create_time"`
	Error           string `json:"error" gorm:"error"`
}

func (*JobEntity) TableName() string {
	return "t_job"
}

type CpInfoEntity struct {
	Id                 int64    `json:"id" gorm:"primaryKey;autoIncrement"`
	NodeId             string   `json:"node_id" gorm:"node_id"`
	OwnerAddress       string   `json:"owner_address" gorm:"owner_address"`
	Beneficiary        string   `json:"beneficiary" gorm:"beneficiary"`
	WorkerAddress      string   `json:"worker_address" gorm:"worker_address"`
	Version            string   `json:"version" gorm:"version"`
	ContractAddress    string   `json:"contract_address" gorm:"contract_address"`
	MultiAddressesJSON string   `gorm:"multi_addresses_json;type:text" json:"-"`
	TaskTypesJSON      string   `gorm:"task_types_json; type:text" json:"-"`
	CreateAt           string   `json:"create_at" gorm:"create_at"`
	UpdateAt           string   `json:"update_at" gorm:"update_at"`
	MultiAddresses     []string `json:"multi_addresses" gorm:"-"`
	TaskTypes          []uint8  `json:"task_types" gorm:"-"` // 1: Fil-C2 2: Aleo 3: AI (Space)

}

func (*CpInfoEntity) TableName() string {
	return "t_cp_info"
}

func (c *CpInfoEntity) BeforeSave(tx *gorm.DB) (err error) {
	if multiAddrBytes, err := json.Marshal(c.MultiAddresses); err == nil {
		c.MultiAddressesJSON = string(multiAddrBytes)
	} else {
		return err
	}

	intTaskTypes := make([]int, len(c.TaskTypes))
	for i, v := range c.TaskTypes {
		intTaskTypes[i] = int(v)
	}

	if taskTypesBytes, err := json.Marshal(intTaskTypes); err == nil {
		c.TaskTypesJSON = string(taskTypesBytes)
	} else {
		return err
	}
	return nil
}

func (c *CpInfoEntity) AfterFind(tx *gorm.DB) (err error) {
	if err = json.Unmarshal([]byte(c.MultiAddressesJSON), &c.MultiAddresses); err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(c.TaskTypesJSON), &c.TaskTypes); err != nil {
		return err
	}
	return nil
}
