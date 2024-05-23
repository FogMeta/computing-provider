package initializer

import (
	"encoding/json"
	"fmt"
	"github.com/filswan/go-swan-lib/logs"
	"github.com/swanchain/go-computing-provider/conf"
	"github.com/swanchain/go-computing-provider/internal/computing"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

func sendHeartbeat(nodeId, ownerAddress string) {
	heartbeatURL := conf.GetConfig().HUB.ServerUrl + "/cp/heartbeat"
	payload := strings.NewReader(fmt.Sprintf(`{
	"public_address": "%s",
    "node_id": "%s",
    "status": "Active"
}`, ownerAddress, nodeId))

	client := &http.Client{}
	req, err := http.NewRequest("POST", heartbeatURL, payload)
	if err != nil {
		logs.GetLogger().Errorf("Error creating request: %v", err)
		return
	}
	// Set the API token in the request header (replace "your_api_token" with the actual token)
	req.Header.Set("Authorization", "Bearer "+conf.GetConfig().HUB.AccessToken)
	req.Header.Add("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		logs.GetLogger().Errorf("Error sending heartbeat, retrying to connect to the Swan Hub server: %v", err)
		computing.Reconnect(nodeId)
	} else {
		data, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			logs.GetLogger().Errorf("send heartbeat read response failed, error: %v", err)
			return
		}
		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusNotFound {
				var respData struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(data, &respData)
				logs.GetLogger().Warningln("resp status:", resp.StatusCode, "error:", respData.Message, "retrying to connect to the Swan Hub server")
			} else {
				logs.GetLogger().Warningln("resp status:", resp.StatusCode, "error:", string(data), "retrying to connect to the Swan Hub server")
			}
			computing.Reconnect(nodeId)
		}
	}
}

func SendHeartbeats(nodeId string) {
	ownerAddress, _, err := computing.GetOwnerAddressAndWorkerAddress()
	if err != nil {
		logs.GetLogger().Fatalf("get owner address failed, error: %v", err)
		return
	}

	ticker := time.NewTicker(15 * time.Second)
	for range ticker.C {
		sendHeartbeat(nodeId, ownerAddress)
	}
}

func ProjectInit(cpRepoPath string) {
	if err := conf.InitConfig(cpRepoPath, false); err != nil {
		logs.GetLogger().Fatal(err)
	}
	nodeID := computing.InitComputingProvider(cpRepoPath)
	// Start sending heartbeats
	go SendHeartbeats(nodeID)

	computing.NewCronTask(nodeID).RunTask()

}
