package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/filswan/go-mcs-sdk/mcs/api/common/logs"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/itsjamie/gin-cors"
	"github.com/lagrangedao/go-computing-provider/account"
	"github.com/lagrangedao/go-computing-provider/conf"
	"github.com/lagrangedao/go-computing-provider/internal/computing"
	"github.com/lagrangedao/go-computing-provider/internal/initializer"
	"github.com/lagrangedao/go-computing-provider/util"
	"github.com/lagrangedao/go-computing-provider/wallet"
	"github.com/urfave/cli/v2"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var runCmd = &cli.Command{
	Name:  "run",
	Usage: "Start a cp process",
	Action: func(cctx *cli.Context) error {
		logs.GetLogger().Info("Start in computing provider mode.")

		cpRepoPath := cctx.String(FlagCpRepo)
		os.Setenv("CP_PATH", cpRepoPath)
		initializer.ProjectInit(cpRepoPath)

		r := gin.Default()
		r.Use(cors.Middleware(cors.Config{
			Origins:         "*",
			Methods:         "GET, PUT, POST, DELETE",
			RequestHeaders:  "Origin, Authorization, Content-Type",
			ExposedHeaders:  "",
			MaxAge:          50 * time.Second,
			ValidateHeaders: false,
		}))
		pprof.Register(r)

		v1 := r.Group("/api/v1")
		cpManager(v1.Group("/computing"))

		shutdownChan := make(chan struct{})
		httpStopper, err := util.ServeHttp(r, "cp-api", ":"+strconv.Itoa(conf.GetConfig().API.Port))
		if err != nil {
			logs.GetLogger().Fatal("failed to start cp-api endpoint: %s", err)
		}

		finishCh := util.MonitorShutdown(shutdownChan,
			util.ShutdownHandler{Component: "cp-api", StopFunc: httpStopper},
		)
		<-finishCh

		return nil
	},
}

func cpManager(router *gin.RouterGroup) {
	router.GET("/host/info", computing.GetServiceProviderInfo)
	router.POST("/lagrange/jobs", computing.ReceiveJob)
	router.POST("/lagrange/jobs/redeploy", computing.RedeployJob)
	router.DELETE("/lagrange/jobs", computing.CancelJob)
	router.GET("/lagrange/cp", computing.StatisticalSources)
	router.POST("/lagrange/jobs/renew", computing.ReNewJob)
	router.GET("/lagrange/spaces/log", computing.GetSpaceLog)
	router.POST("/lagrange/cp/proof", computing.DoProof)

	router.GET("/cp", computing.StatisticalSources)
	router.GET("/cp/info", computing.GetCpInfo)
	router.POST("/cp/ubi", computing.DoUbiTask)
	router.POST("/cp/receive/ubi", computing.ReceiveUbiProof)

}

var initCmd = &cli.Command{
	Name:  "init",
	Usage: "Initialize a new cp",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "ownerAddress",
			Usage: "Specify a OwnerAddress",
		},
		&cli.BoolFlag{
			Name:  "beneficiaryAddress",
			Usage: "Specify a beneficiaryAddress to receive rewards. If not specified, use the same address as ownerAddress",
		},
	},
	Action: func(cctx *cli.Context) error {

		ownerAddress := cctx.String("ownerAddress")
		if strings.TrimSpace(ownerAddress) == "" {
			return fmt.Errorf("ownerAddress is not empty")
		}

		cpRepoPath := cctx.String(FlagCpRepo)
		os.Setenv("CP_PATH", cpRepoPath)
		initializer.ProjectInit(cpRepoPath)

		chainUrl, err := conf.GetRpcByName(conf.DefaultRpc)
		if err != nil {
			return fmt.Errorf("get rpc url failed, error: %v", err)
		}

		localWallet, err := wallet.SetupWallet(wallet.WalletRepo)
		if err != nil {
			return fmt.Errorf("setup wallet failed, error: %v", err)
		}

		ki, err := localWallet.FindKey(ownerAddress)
		if err != nil || ki == nil {
			return fmt.Errorf("the address: %s, private key %v", ownerAddress, wallet.ErrKeyInfoNotFound)
		}

		client, err := ethclient.Dial(chainUrl)
		if err != nil {
			return fmt.Errorf("dial rpc connect failed, error: %v", err)
		}
		defer client.Close()

		privateKey, err := crypto.HexToECDSA(ki.PrivateKey)
		if err != nil {
			return err
		}

		publicKey := privateKey.Public()
		publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("failed to cast public key to ECDSA")
		}

		publicAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
		nonce, err := client.PendingNonceAt(context.Background(), publicAddress)
		if err != nil {
			return err
		}

		suggestGasPrice, err := client.SuggestGasPrice(context.Background())
		if err != nil {
			return err
		}

		chainId, _ := client.ChainID(context.Background())
		auth, err := bind.NewKeyedTransactorWithChainID(privateKey, chainId)
		if err != nil {
			return err
		}

		auth.Nonce = big.NewInt(int64(nonce))
		suggestGasPrice = suggestGasPrice.Mul(suggestGasPrice, big.NewInt(3))
		suggestGasPrice = suggestGasPrice.Div(suggestGasPrice, big.NewInt(2))
		auth.GasFeeCap = suggestGasPrice
		auth.Context = context.Background()

		nodeID := computing.InitComputingProvider(cpRepoPath)
		multiAddresses := conf.GetConfig().API.MultiAddress
		var ubiTaskFlag uint8
		if conf.GetConfig().API.UbiTask {
			ubiTaskFlag = 1
		}
		contractAddress, tx, _, err := account.DeployAccount(auth, client, publicAddress, nodeID, []string{multiAddresses}, ubiTaskFlag, publicAddress)
		if err != nil {
			return fmt.Errorf("deploy cp account contract failed, error: %v", err)
		}

		err = os.WriteFile(filepath.Join(cpRepoPath, "account"), []byte(contractAddress.Hex()), 0666)
		if err != nil {
			return fmt.Errorf("write cp account contract address failed, error: %v", err)
		}

		fmt.Printf("Contract deployed! Address: %s\n", contractAddress.Hex())
		fmt.Printf("Transaction hash: %s\n", tx.Hash().Hex())
		return nil
	},
}

var changeMultiAddressCmd = &cli.Command{
	Name:      "changeMultiAddress",
	Usage:     "Update CP of MultiAddress",
	ArgsUsage: "[multiAddress] example: /ip4/<public_ip>/tcp/<port>.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "ownerAddress",
			Usage: "Specify a OwnerAddress",
		},
	},
	Action: func(cctx *cli.Context) error {
		if cctx.NArg() != 1 {
			return fmt.Errorf(" Requires a multiAddress")
		}

		ownerAddress := cctx.String("ownerAddress")
		if strings.TrimSpace(ownerAddress) == "" {
			return fmt.Errorf("ownerAddress is not empty")
		}

		multiAddr := cctx.Args().Get(0)
		if strings.TrimSpace(multiAddr) == "" {
			return fmt.Errorf("failed to parse : %s", multiAddr)
		}

		cpRepoPath := cctx.String(FlagCpRepo)
		os.Setenv("CP_PATH", cpRepoPath)
		initializer.ProjectInit(cpRepoPath)

		chainUrl, err := conf.GetRpcByName(conf.DefaultRpc)
		if err != nil {
			logs.GetLogger().Errorf("get rpc url failed, error: %v,", err)
			return err
		}

		localWallet, err := wallet.SetupWallet(wallet.WalletRepo)
		if err != nil {
			logs.GetLogger().Errorf("setup wallet ubi failed, error: %v,", err)
			return err
		}

		ki, err := localWallet.FindKey(ownerAddress)
		if err != nil || ki == nil {
			logs.GetLogger().Errorf("the address: %s, private key %w,", conf.GetConfig().HUB.WalletAddress, wallet.ErrKeyInfoNotFound)
			return err
		}

		client, err := ethclient.Dial(chainUrl)
		if err != nil {
			logs.GetLogger().Errorf("dial rpc connect failed, error: %v,", err)
			return err
		}
		defer client.Close()

		cpStub, err := account.NewAccountStub(client, account.WithCpPrivateKey(ki.PrivateKey))
		if err != nil {
			logs.GetLogger().Errorf("create ubi task client failed, error: %v,", err)
			return err
		}

		owner, err := cpStub.GetOwner()
		if err != nil {
			return fmt.Errorf("get ownerAddress faile, error: %v", err)
		}
		if !strings.EqualFold(owner, ownerAddress) {
			return fmt.Errorf("the owner address is incorrect. The owner on the chain is %s, and the current address is %s", owner, ownerAddress)
		}

		submitUBIProofTx, err := cpStub.ChangeMultiAddress([]string{multiAddr})
		if err != nil {
			logs.GetLogger().Errorf("change multi-addr tx failed, error: %v,", err)
			return err
		}
		fmt.Printf("ChangeMultiAddress: %s", submitUBIProofTx)

		return nil
	},
}

var ChangeOwnerAddressCmd = &cli.Command{
	Name:      "change-owner-addr",
	Usage:     "Update CP of OwnerAddress",
	ArgsUsage: "[oldOwnerAddress] newOwnerAddress",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "ownerAddress",
			Usage: "Specify a OwnerAddress",
		},
	},
	Action: func(cctx *cli.Context) error {

		ownerAddress := cctx.String("ownerAddress")
		if strings.TrimSpace(ownerAddress) == "" {
			return fmt.Errorf("ownerAddress is not empty")
		}

		if cctx.NArg() != 1 {
			return fmt.Errorf(" Requires a multiAddress")
		}

		newOwnerAddr := cctx.Args().Get(0)
		if strings.TrimSpace(newOwnerAddr) == "" {
			return fmt.Errorf("failed to parse : %s", newOwnerAddr)
		}

		cpRepoPath := cctx.String(FlagCpRepo)
		os.Setenv("CP_PATH", cpRepoPath)
		initializer.ProjectInit(cpRepoPath)

		chainUrl, err := conf.GetRpcByName(conf.DefaultRpc)
		if err != nil {
			logs.GetLogger().Errorf("get rpc url failed, error: %v,", err)
			return err
		}

		localWallet, err := wallet.SetupWallet(wallet.WalletRepo)
		if err != nil {
			logs.GetLogger().Errorf("setup wallet ubi failed, error: %v,", err)
			return err
		}

		ki, err := localWallet.FindKey(ownerAddress)
		if err != nil || ki == nil {
			logs.GetLogger().Errorf("the address: %s, private key %w,", conf.GetConfig().HUB.WalletAddress, wallet.ErrKeyInfoNotFound)
			return err
		}

		client, err := ethclient.Dial(chainUrl)
		if err != nil {
			logs.GetLogger().Errorf("dial rpc connect failed, error: %v,", err)
			return err
		}
		defer client.Close()

		cpStub, err := account.NewAccountStub(client, account.WithCpPrivateKey(ki.PrivateKey))
		if err != nil {
			logs.GetLogger().Errorf("create cp client failed, error: %v,", err)
			return err
		}

		owner, err := cpStub.GetOwner()
		if err != nil {
			return fmt.Errorf("get ownerAddress faile, error: %v", err)
		}
		if !strings.EqualFold(owner, ownerAddress) {
			return fmt.Errorf("the owner address is incorrect. The owner on the chain is %s, and the current address is %s", owner, ownerAddress)
		}

		submitUBIProofTx, err := cpStub.ChangeOwnerAddress(common.HexToAddress(newOwnerAddr))
		if err != nil {
			logs.GetLogger().Errorf("change owner address tx failed, error: %v,", err)
			return err
		}
		fmt.Printf("ChangeOwnerAddress: %s", submitUBIProofTx)

		return nil
	},
}

var ChangeBeneficiaryAddressCmd = &cli.Command{
	Name:      "changeBeneficiaryAddress",
	Usage:     "Update CP of beneficiaryAddress",
	ArgsUsage: "[beneficiaryAddress]",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "ownerAddress",
			Usage: "Specify a OwnerAddress",
		},
	},
	Action: func(cctx *cli.Context) error {

		ownerAddress := cctx.String("ownerAddress")
		if strings.TrimSpace(ownerAddress) == "" {
			return fmt.Errorf("ownerAddress is not empty")
		}

		if cctx.NArg() != 1 {
			return fmt.Errorf(" Requires a beneficiaryAddress")
		}

		beneficiaryAddress := cctx.Args().Get(0)
		if strings.TrimSpace(beneficiaryAddress) == "" {
			return fmt.Errorf("failed to parse target address: %s", beneficiaryAddress)
		}

		cpRepoPath := cctx.String(FlagCpRepo)
		os.Setenv("CP_PATH", cpRepoPath)
		initializer.ProjectInit(cpRepoPath)

		chainUrl, err := conf.GetRpcByName(conf.DefaultRpc)
		if err != nil {
			logs.GetLogger().Errorf("get rpc url failed, error: %v,", err)
			return err
		}

		localWallet, err := wallet.SetupWallet(wallet.WalletRepo)
		if err != nil {
			logs.GetLogger().Errorf("setup wallet ubi failed, error: %v,", err)
			return err
		}

		ki, err := localWallet.FindKey(ownerAddress)
		if err != nil || ki == nil {
			logs.GetLogger().Errorf("the address: %s, private key %v. Please import the address into the wallet", conf.GetConfig().HUB.WalletAddress, wallet.ErrKeyInfoNotFound)
			return err
		}

		client, err := ethclient.Dial(chainUrl)
		if err != nil {
			logs.GetLogger().Errorf("dial rpc connect failed, error: %v,", err)
			return err
		}
		defer client.Close()

		cpStub, err := account.NewAccountStub(client, account.WithCpPrivateKey(ki.PrivateKey))
		if err != nil {
			logs.GetLogger().Errorf("create cp client failed, error: %v,", err)
			return err
		}

		owner, err := cpStub.GetOwner()
		if err != nil {
			return fmt.Errorf("get ownerAddress faile, error: %v", err)
		}
		if !strings.EqualFold(owner, ownerAddress) {
			return fmt.Errorf("the owner address is incorrect. The owner on the chain is %s, and the current address is %s", owner, ownerAddress)
		}
		newQuota := big.NewInt(int64(0))
		newExpiration := big.NewInt(int64(0))
		changeBeneficiaryAddressTx, err := cpStub.ChangeBeneficiary(common.HexToAddress(beneficiaryAddress), newQuota, newExpiration)
		if err != nil {
			logs.GetLogger().Errorf("change owner address tx failed, error: %v,", err)
			return err
		}
		fmt.Printf("changeBeneficiaryAddress Transaction hash: %s", changeBeneficiaryAddressTx)
		return nil
	},
}
