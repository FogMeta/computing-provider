// Code generated by Wire. DO NOT EDIT.

//go:generate go run -mod=mod github.com/google/wire/cmd/wire
//go:build !wireinject
// +build !wireinject

package computing

import (
	"github.com/swanchain/go-computing-provider/internal/db"
)

import (
	_ "unsafe"
)

// Injectors from wire.go:

func NewTaskService() TaskService {
	gormDB := db.NewDbService()
	taskService := TaskService{
		DB: gormDB,
	}
	return taskService
}

func NewJobService() JobService {
	gormDB := db.NewDbService()
	jobService := JobService{
		DB: gormDB,
	}
	return jobService
}

func NewCpInfoService() CpInfoService {
	gormDB := db.NewDbService()
	cpInfoService := CpInfoService{
		DB: gormDB,
	}
	return cpInfoService
}
