package service

import (
	"context"
	"log"
	"os"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type K8SClient struct {
	cli *kubernetes.Clientset
}

type PortInfo struct {
	Protocol   string
	Port       int32
	NodePort   int32
	TargetPort string
}

type Service struct {
	SvcName   string
	ClusterIp string
	Ports     []PortInfo
}

func NewK8SClient(kubeconfig string) *K8SClient {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig == "" {
		home := homedir.HomeDir()
		loadingRules.ExplicitPath = filepath.Join(home, ".kube", "config")
	} else {
		loadingRules.ExplicitPath = kubeconfig
	}
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		log.Printf("Failed to create client config: %v\n", err)
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("Failed to create clientset: %v\n", err)
		os.Exit(1)
	}
	return &K8SClient{
		cli: clientset,
	}
}

func (k8s *K8SClient) GetNamespaceServices(namespace string) []Service {
	var ret []Service
	services, err := k8s.cli.CoreV1().Services(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Printf("Failed to list services: %v\n", err)
		os.Exit(1)
	}
	for _, svc := range services.Items {
		cur := Service{
			SvcName:   svc.ObjectMeta.Name,
			ClusterIp: svc.Spec.ClusterIP,
			Ports:     []PortInfo{},
		}
		for _, port := range svc.Spec.Ports {
			p := PortInfo{
				Protocol:   string(port.Protocol),
				Port:       port.Port,
				NodePort:   port.NodePort,
				TargetPort: port.TargetPort.String(),
			}
			cur.Ports = append(cur.Ports, p)
		}
		ret = append(ret, cur)
	}
	return ret
}