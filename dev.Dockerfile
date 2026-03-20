FROM ubuntu:latest

RUN apt-get update && apt-get install -y ca-certificates

USER 65532:65532

ADD bin/manager_linux /manager
