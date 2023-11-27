#! /bin/bash

DEPLOY="proxy-deployment"
PROXY_SERVICE="proxy-service"
CLUSTER_SERVICE="cluster-service"
SELECTOR_APP="proxy-app"
NAMESPACE="smart-agent"
CONFIGMAP="client-map"
CLUSTERROLE="configmap-reader"
use_k8s=0
if command -v kubectl >/dev/null 2>&1; then
	K="kubectl"
	use_k8s=1
	echo "use kubernetes"
else
	K="minikube kubectl --"
	echo "use minikube"
fi

printHelp() {
    echo 'Usage: ./run.sh [build|deploy|show|clean|log|ssh]'
	echo '  ./run.sh build'
	echo '      build agent container'
    echo '  ./run.sh deploy n'
    echo '      deploy n proxy agents in k8s'
    echo '  ./run.sh show'
    echo '      display all services and deployments in target namespace'
    echo '  ./run.sh clean'
    echo '      clean all services and deployments in k8s'
    echo '  ./run.sh log i'
    echo "      display the ith agent's log"
    echo '  ./run.sh ssh i'
    echo "      use ssh to connect into the ith agent"
}

createResourceNeeded() {
    ns_cnt=$($K get ns | grep ${NAMESPACE} | wc -l)
    if [[ $ns_cnt -eq 0 ]]; then
        echo "create namespace ${NAMESPACE}"
        $K create ns ${NAMESPACE}
    fi
    cm_cnt=$($K get configmap -n ${NAMESPACE} | grep ${CONFIGMAP} | wc -l)
    if [[ $cm_cnt -eq 0 ]]; then
        echo "create configmap ${CONFIGMAP}"
        $K create configmap ${CONFIGMAP} --from-file=configmap.yaml -n ${NAMESPACE}
    fi  
    rb_cnt=$($K get clusterrole | grep ${CLUSTERROLE} | wc -l)
    if [[ $rb_cnt -eq 0 ]]; then
        echo "apply role binding policy"
        $K apply -f clusterrolebinding.yaml
    fi
}

build() {
    echo "Running build container operation..."
	if [[ $use_k8s -eq 0 ]]; then
        eval $(minikube docker-env)
	fi
    echo "build server..."
    go build -o server cmd/server/main.go
    echo "build docker image..."
    docker build -t my-agent .
}

deployNAgents() {
    for (( i = 1; i <= $1; i++ )); do
        printf "build ${DEPLOY}%d\n" $i
		deploy_file="deployment_minikube.yaml"
		if [[ $use_k8s -eq 1 ]]; then
			deploy_file="deployment_k8s.yaml"
		fi
        deploy_temp=$(mktemp)
        sed "s/${DEPLOY}/\0${i}/" $deploy_file > $deploy_temp
        sed -i "s/${PROXY_SERVICE}/\0${i}/" $deploy_temp
        sed -i "s/${CLUSTER_SERVICE}/\0${i}/" $deploy_temp
        sed -i "s/${SELECTOR_APP}/\0${i}/" $deploy_temp
        $K apply -f $deploy_temp
        rm $deploy_temp
    done
}

clean() {
    echo "Running clean operation..."
    echo "delete all deployments and services..."
    running_services=$($K get svc -n $NAMESPACE | grep $PROXY_SERVICE | awk '{print $1}')
    while IFS= read -r service; do
        deploy=$(echo $service | sed 's/service/deployment/')
        $K delete service $service -n $NAMESPACE
        $K delete deployment $deploy -n $NAMESPACE
    done <<< $running_services
    running_services=$($K get svc -n $NAMESPACE | grep $CLUSTER_SERVICE | awk '{print $1}')
    while IFS= read -r service; do
        $K delete service $service -n $NAMESPACE
    done <<< $running_services
    echo "delete configmaps..."
    $K delete configmap ${CONFIGMAP} -n ${NAMESPACE}
}

if [[ "$1" == "build" ]]; then
    build
elif [[ "$1" == "deploy" ]]; then
    createResourceNeeded
    deployNAgents $2
elif [[ "$1" == "show" ]]; then
    echo "Services:"
    $K get svc -n ${NAMESPACE}
    echo "Deployments:"
    $K get deployment -n ${NAMESPACE}
    echo "Configmap:"
    $K get configmap $CONFIGMAP -n ${NAMESPACE} -o jsonpath='{.data}'
elif [[ "$1" == "clean" ]]; then
    clean
elif [[ "$1" == "log" ]]; then
    [[ $# < 2 ]] && exit 0
    podname=$($K get pods -n ${NAMESPACE} | grep "${DEPLOY}${2}" | awk '{print $1}')
    # echo $podname
    $K logs -f $podname -n ${NAMESPACE}
elif [[ "$1" == "ssh" ]]; then
    [[ $# < 2 ]] && exit 0
    podname=$($K get pods -n ${NAMESPACE} | grep "${DEPLOY}${2}" | awk '{print $1}')
    $K exec -it $podname -n ${NAMESPACE} -- /bin/sh
else
    printHelp
fi
