FROM golang:1.22-bullseye AS builder
WORKDIR /usr/src/pulumi-hcloud-kubeadm
COPY ./go/go.mod ./go/go.sum ./
RUN go mod download && go mod verify
COPY ./go/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -v -o /usr/local/bin/pulumi-hcloud-kubeadm --ldflags "-s -w" .

FROM debian:stable-slim
ARG GO_VERSION="1.22.4"
ARG PULUMI_VERSION="3.119.0"

RUN apt-get update && apt-get install -y python3 python3-pip python3-venv bash curl wget vim jq pipx openssl openssh-client sshpass netcat-openbsd && \
    rm -rf /var/lib/apt/lists/* && \
    rm -rf $(python3 -c "import sysconfig; print(sysconfig.get_path(\"stdlib\"))")/EXTERNALLY-MANAGED

RUN useradd -m pulumi-hcloud-kubeadm -s /bin/bash
RUN curl https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz -Lo /tmp/go${GO_VERSION}.linux-amd64.tar.gz && \
    tar -zxf /tmp/go${GO_VERSION}.linux-amd64.tar.gz -C /usr/local  && \
    rm -rf /tmp/go${GO_VERSION}.linux-amd64.tar.gz

COPY ./ansible/ansible.cfg /etc/ansible/ansible.cfg

USER pulumi-hcloud-kubeadm

RUN pipx install --include-deps ansible && \
    pipx ensurepath
RUN mkdir ~/.pulumi && \ 
    curl https://get.pulumi.com/releases/sdk/pulumi-v${PULUMI_VERSION}-linux-x64.tar.gz -Lo /tmp/pulumi-v${PULUMI_VERSION}-linux-x64.tar.gz && \
    tar -zxf /tmp/pulumi-v${PULUMI_VERSION}-linux-x64.tar.gz -C /tmp && \
    cp -r /tmp/pulumi/. ~/.pulumi/bin/ && \
    rm -rf /tmp/pulumi* && \
    echo "export PATH=\$PATH:\$HOME/.pulumi/bin" >> ~/.bashrc && \
    echo "export PATH=\$PATH:/usr/local/go/bin" >> ~/.bashrc && \
    mkdir -p ~/.ansible

WORKDIR /home/pulumi-hcloud-kubeadm

ENV PULUMI_HOME=/home/pulumi-hcloud-kubeadm/.pulumi \
    PULUMI_SKIP_UPDATE_CHECK=true \
    PULUMI_K8S_ENABLE_PATCH_FORCE=true \
    PATH=${PATH}:/usr/local/go/bin:/home/pulumi-hcloud-kubeadm/.pulumi/bin:/home/pulumi-hcloud-kubeadm/.local/bin

COPY --chown=pulumi-hcloud-kubeadm:pulumi-hcloud-kubeadm --from=builder /usr/local/bin/pulumi-hcloud-kubeadm /home/pulumi-hcloud-kubeadm/bin/
COPY --chown=pulumi-hcloud-kubeadm:pulumi-hcloud-kubeadm build/Pulumi.yaml .
COPY --chown=pulumi-hcloud-kubeadm:pulumi-hcloud-kubeadm build/init /usr/local/bin/init
COPY --chown=pulumi-hcloud-kubeadm:pulumi-hcloud-kubeadm build/topology.yaml /home/pulumi-hcloud-kubeadm/topology.yaml.example
COPY --chown=pulumi-hcloud-kubeadm:pulumi-hcloud-kubeadm ./ansible /home/pulumi-hcloud-kubeadm/.ansible

RUN mkdir -p ./vars && \
    pulumi login --local && \
    pulumi plugin install resource command v0.11.1 --exact && \
    pulumi plugin install resource hcloud v1.19.1 --exact && \
    pulumi plugin install resource tls v5.0.3 --exact

VOLUME /home/pulumi-hcloud-kubeadm/vars

ENTRYPOINT ["/usr/local/bin/init"]