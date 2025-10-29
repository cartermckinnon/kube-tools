package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"k8s.io/kubernetes/pkg/kubelet/cadvisor"
)

func main() {
	client, err := cadvisor.New(&unsupportedImageFsInfoProvider{}, "/", []string{}, false /* usingLegacyStats */, false /* localStorageCapacityIsolation */)
	if err != nil {
		log.Fatalf("failed to setup cadvisor: %v", err)
	}
	info, err := client.MachineInfo()
	if err != nil {
		log.Fatalf("failed to get machine info: %v", err)
	}
	log.Printf("Machine Info: %+v", info)
	infoJson, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		log.Fatalf("failed to marshal machine info: %v", err)
	}
	fmt.Println(infoJson)
}

// unsupportedImageFsInfoProvider is a no-op implementation of ImageFsInfoProvider.
type unsupportedImageFsInfoProvider struct{}

func (i *unsupportedImageFsInfoProvider) ImageFsInfoLabel() (string, error) {
	return "", errors.New("unsupported")
}

func (i *unsupportedImageFsInfoProvider) ContainerFsInfoLabel() (string, error) {
	return "", errors.New("unsupported")
}
