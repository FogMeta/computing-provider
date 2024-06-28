package test

import (
	"archive/tar"
	"context"
	"fmt"
	"github.com/swanchain/go-computing-provider/internal/common"
	"github.com/swanchain/go-computing-provider/internal/yaml"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestNewK8sService(t *testing.T) {
	service := common.NewK8sService()
	service.GetPods("kube-system", "")
}

func TestTar(t *testing.T) {
	buildPath := "build/0xe259F84193604f9c8228940Ab5cB5c62Dfb514d6/spaces/demo001"
	spaceName := "DEMO-123"
	file, err := os.Create(fmt.Sprintf("/tmp/build/%s.tar", spaceName))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer file.Close()

	tarWriter := tar.NewWriter(file)
	defer tarWriter.Close()
	filepath.Walk(buildPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Println(err)
			return err
		}
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			fmt.Println(err)
			return err
		}
		relPath, err := filepath.Rel(buildPath, path)
		if err != nil {
			fmt.Println(err)
			return err
		}
		header.Name = relPath
		if err := tarWriter.WriteHeader(header); err != nil {
			fmt.Println(err)
			return err
		}

		if !info.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				fmt.Println(err)
				return err
			}
			defer file.Close()
			if _, err := io.Copy(tarWriter, file); err != nil {
				fmt.Println(err)
				return err
			}
		}
		return nil
	})

	fmt.Println("Archive created successfully!")
}

func TestDockerBuild(t *testing.T) {
	dockerService := common.NewDockerService()
	dockerService.CleanResource()
}

func TestNewStorageService(t *testing.T) {
	service, err := common.NewStorageService()
	if err != nil {
		fmt.Println(err)
		return
	}

	path := "/Users/sonic/Documents/python_space/go-computing-provider/cp-cache/jobs/ea015a0d-c78b-4c0e-9103-99fbc8818d89.json"
	ossFile, err := service.UploadFileToBucket("demo.json", path, false)
	if err != nil {
		log.Fatalln(err)
	}
	log.Printf("%+v \n", ossFile)

	service.GetGatewayUrl()
	//service.DeleteBucket("demo")

	//service.CreateBucket("demo")

	//service.CreateFolder("jobs")

}

func TestYamlToK8s(t *testing.T) {
	containerResources, err := yaml.HandlerYaml("/Users/zhanglong/Documents/go-computing-provider/dd.yaml")
	if err != nil {
		log.Fatalln(err)
	}
	log.Printf("%+v", containerResources)
}

func TestStatisticalSources(t *testing.T) {
	service := common.NewK8sService()
	_, err := service.StatisticalSources(context.TODO())
	if err != nil {
		log.Fatalln(err)
	}
}
