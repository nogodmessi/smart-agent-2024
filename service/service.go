package service

import (
	"context"
	"log"
	"os"
	"smart-agent/config"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type K8SClient struct {
	cli *kubernetes.Clientset
}

type PortInfo struct {
	Name       string
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

type Pod struct {
	PodName string
	PodIP   string
}

func NewK8SClient(kubeconfig string) *K8SClient {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = kubeconfig
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		log.Fatalln("Failed to create client config:", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalln("Failed to create clientset:", err)
	}
	return &K8SClient{
		cli: clientset,
	}
}

func NewK8SClientInCluster() *K8SClient {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalln("Fail to create in cluster config:", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalln("Failed to create clientset:", err)
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
				Name:       port.Name,
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

func (k8s *K8SClient) GetNameSpacePods(namespace string) []Pod {
	var ret []Pod
	pods, err := k8s.cli.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Fatalln("Error getting pods: ", err)
		os.Exit(1)
	}
	prefix := "proxy-deployment" //匹配前缀
	templateName := "proxy-service"
	for _, pod := range pods.Items {
		if ip := pod.Status.PodIP; ip != "" && len(pod.Name) >= len(prefix) && pod.Name[:len(prefix)] == prefix {
			newName := templateName + string(pod.Name[len(prefix)])
			cur := Pod{
				PodIP:   ip,
				PodName: newName,
			}
			ret = append(ret, cur)
		}
	}
	return ret
}

func (k8s *K8SClient) createConfigMap(data map[string]string) error {
	// Create a new ConfigMap object with the desired key-value pairs.
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.EtcdClientMapName,
			Namespace: config.Namespace,
		},
		Data: data,
	}

	// Create the ConfigMap in the specified namespace.
	_, err := k8s.cli.CoreV1().ConfigMaps(config.Namespace).Create(context.TODO(), configMap, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	return nil
}

func (k8s *K8SClient) EtcdPut(key, value string) error {
	cm, err := k8s.cli.CoreV1().ConfigMaps(config.Namespace).Get(context.TODO(), config.EtcdClientMapName, metav1.GetOptions{})
	if err != nil {
		errmsg := err.Error()
		if strings.HasPrefix(errmsg, "configmaps") && strings.HasSuffix(errmsg, "not found") {
			return k8s.createConfigMap(map[string]string{key: value})
		}
		return err
	}
	cm.Data[key] = value
	_, err = k8s.cli.CoreV1().ConfigMaps(config.Namespace).Update(context.TODO(), cm, metav1.UpdateOptions{})
	return err
}

func (k8s *K8SClient) EtcdGet(key string) (string, error) {
	cm, err := k8s.cli.CoreV1().ConfigMaps(config.Namespace).Get(context.TODO(), config.EtcdClientMapName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	data := cm.Data[key]
	return data, nil
}

func (k8s *K8SClient) EtcdDelete(key string) error {
	cm, err := k8s.cli.CoreV1().ConfigMaps(config.Namespace).Get(context.TODO(), config.EtcdClientMapName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	delete(cm.Data, key)
	_, err = k8s.cli.CoreV1().ConfigMaps(config.Namespace).Update(context.TODO(), cm, metav1.UpdateOptions{})
	return err
}
