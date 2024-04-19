FROM ubuntu:latest

RUN sed -i 's/archive.ubuntu.com/mirrors.ustc.edu.cn/g' /etc/apt/sources.list
RUN apt-get update && apt-get install -y redis-server inetutils-ping netcat net-tools

COPY server /app/server

WORKDIR /app
CMD redis-server --port 7777 & \
    sleep 1 && \
    ./server 2>&1
