package computing

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/swanchain/go-computing-provider/account"
	"log"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/filswan/go-mcs-sdk/mcs/api/common/logs"
	"github.com/swanchain/go-computing-provider/conf"
)

func InitComputingProvider(cpRepoPath string) string {
	nodeID, peerID, address := GenerateNodeID(cpRepoPath)

	logs.GetLogger().Infof("Node ID :%s Peer ID:%s address:%s",
		nodeID, peerID, address)
	return nodeID
}

func GetNodeId(cpRepoPath string) string {
	nodeID, _, _ := GenerateNodeID(cpRepoPath)
	return nodeID
}

func GenerateNodeID(cpRepoPath string) (string, string, string) {
	privateKeyPath := filepath.Join(cpRepoPath, "private_key")
	var privateKeyBytes []byte

	if _, err := os.Stat(privateKeyPath); err == nil {
		privateKeyBytes, err = os.ReadFile(privateKeyPath)
		if err != nil {
			log.Fatalf("Error reading private key: %v", err)
		}
	} else {
		privateKeyBytes = make([]byte, 32)
		_, err := rand.Read(privateKeyBytes)
		if err != nil {
			log.Fatalf("Error generating random key: %v", err)
		}

		err = os.MkdirAll(filepath.Dir(privateKeyPath), os.ModePerm)
		if err != nil {
			log.Fatalf("Error creating directory for private key: %v", err)
		}

		err = os.WriteFile(privateKeyPath, privateKeyBytes, 0644)
		if err != nil {
			log.Fatalf("Error writing private key: %v", err)
		}
	}

	privateKey, err := crypto.ToECDSA(privateKeyBytes)
	if err != nil {
		log.Fatalf("Error converting private key bytes: %v", err)
	}
	nodeID := hex.EncodeToString(crypto.FromECDSAPub(&privateKey.PublicKey))
	peerID := hashPublicKey(&privateKey.PublicKey)
	address := crypto.PubkeyToAddress(privateKey.PublicKey).String()
	return nodeID, peerID, address
}

func hashPublicKey(publicKey *ecdsa.PublicKey) string {
	publicKeyBytes := crypto.FromECDSAPub(publicKey)
	hash := sha256.Sum256(publicKeyBytes)
	return hex.EncodeToString(hash[:])
}

func GetOwnerAddressAndWorkerAddress() (string, string, error) {
	chainRpc, err := conf.GetRpcByName(conf.DefaultRpc)
	if err != nil {
		logs.GetLogger().Errorf("get rpc link failed, error: %v", err)
		return "", "", err
	}
	client, err := ethclient.Dial(chainRpc)
	if err != nil {
		logs.GetLogger().Errorf("connect to rpc failed, error: %v", err)
		return "", "", err
	}
	defer client.Close()

	cpStub, err := account.NewAccountStub(client)
	if err != nil {
		logs.GetLogger().Errorf("create account stub failed, error: %v", err)
		return "", "", err
	}
	cpAccount, err := cpStub.GetCpAccountInfo()
	if err != nil {
		err = fmt.Errorf("get cpAccount failed, error: %v", err)
		return "", "", err
	}
	ownerAddress := cpAccount.OwnerAddress
	workerAddress := cpAccount.WorkerAddress
	return ownerAddress, workerAddress, nil
}
