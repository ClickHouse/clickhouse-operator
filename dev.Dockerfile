FROM ubuntu:latest

RUN apt-get update && apt-get install -y ca-certificates

ARG USER_ID=65532
USER $USER_ID:$USER_ID

ADD bin/manager_linux /manager
