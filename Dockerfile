FROM ubuntu:latest

RUN sed -i 's/archive.ubuntu.com/mirrors.ustc.edu.cn/g' /etc/apt/sources.list
RUN apt-get update && apt-get install -y redis-server inetutils-ping netcat net-tools iperf

COPY server /app/server

WORKDIR /app
CMD redis-server --port 7777 & \
    sleep 2 && \
    iperf -s & \
    sleep 1 && \
    ./server 2>&1
