package main

import (
	"log"

	"github.com/advait/pvp-settlement/chaincode/contract"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

func main() {
	cc, err := contractapi.NewChaincode(&contract.PvPContract{})
	if err != nil {
		log.Panicf("creating pvp chaincode: %v", err)
	}
	if err := cc.Start(); err != nil {
		log.Panicf("starting pvp chaincode: %v", err)
	}
}
