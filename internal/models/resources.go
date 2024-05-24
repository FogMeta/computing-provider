package models

type ClusterResource struct {
	NodeId        string          `json:"node_id,omitempty"`
	Region        string          `json:"region,omitempty"`
	ClusterInfo   []*NodeResource `json:"cluster_info"`
	PublicAddress string          `json:"public_address,omitempty"`
	MultiAddress  string          `json:"multi_address,omitempty"`
	NodeName      string          `json:"node_name,omitempty"`
}

type NodeResource struct {
	MachineId string `json:"machine_id"`
	CpuName   string `json:"cpu_name"`
	Cpu       Common `json:"cpu"`
	Vcpu      Common `json:"vcpu"`
	Memory    Common `json:"memory"`
	Gpu       Gpu    `json:"gpu"`
	Storage   Common `json:"storage"`
}

type CollectNodeInfo struct {
	Gpu     Gpu    `json:"gpu"`
	CpuName string `json:"cpu_name"`
}
type Gpu struct {
	DriverVersion string      `json:"driver_version"`
	CudaVersion   string      `json:"cuda_version"`
	AttachedGpus  int         `json:"attached_gpus"`
	Details       []GpuDetail `json:"details"`
}

type GpuDetail struct {
	ProductName     string    `json:"product_name"`
	Status          GpuStatus `json:"status"`
	FbMemoryUsage   Common    `json:"fb_memory_usage"`
	Bar1MemoryUsage Common    `json:"bar1_memory_usage"`
}

type Common struct {
	Total string `json:"total"`
	Used  string `json:"used"`
	Free  string `json:"free"`
}

type ResourceStatus struct {
	Request  int64
	Capacity int64
}

type GpuStatus string

const (
	Occupied  GpuStatus = "occupied"
	Available GpuStatus = "available"
)

type ResourcePolicy struct {
	Cpu     CpuQuota   `json:"cpu"`
	Gpu     []GpuQuota `json:"gpu"`
	Memory  Quota      `json:"memory"`
	Storage Quota      `json:"storage"`
}

type CpuQuota struct {
	Quota int64 `json:"quota"`
}

type GpuQuota struct {
	Name  string `json:"name"`
	Quota int64  `json:"quota"`
}
type Quota struct {
	Quota int64  `json:"quota"`
	Unit  string `json:"unit"`
}

type T struct {
	Gpu struct {
		DriverVersion string `json:"driver_version"`
		CudaVersion   string `json:"cuda_version"`
		AttachedGpus  int    `json:"attached_gpus"`
		Details       []struct {
			ProductName   string `json:"product_name"`
			FbMemoryUsage struct {
				Total string `json:"total"`
				Used  string `json:"used"`
				Free  string `json:"free"`
			} `json:"fb_memory_usage"`
			Bar1MemoryUsage struct {
				Total string `json:"total"`
				Used  string `json:"used"`
				Free  string `json:"free"`
			} `json:"bar1_memory_usage"`
		} `json:"details"`
	} `json:"gpu"`
	MachineId string `json:"machine_id"`
	CpuName   string `json:"cpu_name"`
	Cpu       struct {
		Total string `json:"total"`
		Used  string `json:"used"`
		Free  string `json:"free"`
	} `json:"cpu"`
	Vcpu struct {
		Total string `json:"total"`
		Used  string `json:"used"`
		Free  string `json:"free"`
	} `json:"vcpu"`
	Memory struct {
		Total string `json:"total"`
		Used  string `json:"used"`
		Free  string `json:"free"`
	} `json:"memory"`
	Storage struct {
		Total string `json:"total"`
		Used  string `json:"used"`
		Free  string `json:"free"`
	} `json:"storage"`
}
