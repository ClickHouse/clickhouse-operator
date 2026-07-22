FROM alpine:3

ARG USER_ID=65532
USER $USER_ID:$USER_ID

ADD bin/manager_linux /manager
