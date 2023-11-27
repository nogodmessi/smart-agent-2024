package service

import (
	"fmt"
	"testing"
)

func TestService(t *testing.T) {
	cli := NewK8SClient("/home/yf/.kube/config")
	fmt.Println(cli.GetNameSpacePods("smart-agent"))
}

func TestRemoteK8sService(t *testing.T) {
	cli := NewK8SClient("/home/zds/kubeconfig.yaml")
	for _, svc := range cli.GetNamespaceServices("smart-agent") {
		fmt.Println(svc.SvcName, svc.ClusterIp)
	}
}

func TestReadWrite(t *testing.T) {
	cli := NewK8SClient("/home/zds/.kube/config")
	_ = cli.EtcdPut("client1", "hello world!")
	_ = cli.EtcdPut("client2", "HELLO WORLD!")
	val, _ := cli.EtcdGet("client1")
	fmt.Println("read value:", val)
	val, _ = cli.EtcdGet("client2")
	fmt.Println("read value:", val)
	cli.EtcdDelete("client1")
	val, _ = cli.EtcdGet("client1")
	fmt.Println("read value:", val)
	val, _ = cli.EtcdGet("client2")
	fmt.Println("read value:", val)
}

func TestRead2(t *testing.T) {
	cli := NewK8SClient("/home/zds/.kube/config")
	val, _ := cli.EtcdGet("client1")
	fmt.Println("read value:", val)
	val, _ = cli.EtcdGet("client2")
	fmt.Println("read value:", val)
}
