### 项目架构图
![Uploading image.png…]()


### run servers on minikube

- install `minikube`
- install `go 1.20`

```sh
minikube start
eval $(minikube docker-env)
# show help message using ./run.sh help
./run.sh help
# build target docker container
./run.sh build
# apply serviceAccount.yaml and clusterrolebinding.yaml
kubectl apply -f serviceAccount.yaml
kubectl apply -f clusterrolebinding.yaml
# deploy 3 proxy agents in k8s cluster
./run.sh deploy 3
```

### run servers on kubernetes

```sh
# deploy n proxy agents in kubernetes
./run.sh deploy n
# clean all proxy agents
./run.sh clean
```

### run client

```sh
# build and run client
go build -o client cmd/client/main.go
# create two clients, cli1 send message to cli2
./client -client cli1 --sendto cli2 -config ~/.kube/config
./client -client cli2 --recvfrom cli1 -config ~/.kube/config
# running these programs will display you with a REPL, input .help for help message
```

### workflow

```
client --------> server ---------> prev server
     my client id
     client type
     peer client id
     this cluster ip
     prev cluster ip
                        ---------->
                        fetch old data
                        client id
                        <----------
                        all previous data
       <--------
    all previous data
    transfer end

# if client is sender:
sender ---------> server ---------> peer server --------> receiver
        data
        data
        ...
     peer cluster ip
                        ---------->
                        fresh data
                        my client id
                        all buffered data
                                               ---------->
                                          through connMap[senderId]
                                               all buffered data
    ------------->
        data
                        ------------>
                            data
                                              ------------>
                                                  data
```
