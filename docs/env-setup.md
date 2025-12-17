##### [Back To Main](../README.md)

## Environment Variables Setup

Before running any script, make sure to update the environment variable files according to your system setup.
The environment files are located here:
[Environment vairable(.env) files](../pipeline/)  

**For wfm.sh and wfm-cli.sh script**

Environment file path:-
[WFM Env file](../pipeline/wfm.env)

Update the following variables:
```bash
export GITHUB_USER=<your-github-username>
export GITHUB_TOKEN=<your-github-personal-access-token>
export EXPOSED_HARBOR_IP=<wfm-machine-ip>
export EXPOSED_SYMPHONY_IP=<wfm-machine-ip>
export DEVICE_NODE_IPS="<k3-device-machine-ip:port>,<docker-device-machine-ip:port>" # "172.19.59.148:30999,172.19.59.150:8899"  port:30999 is for k3s device & port:8899 is for docker device
export SYMPHONY_BRANCH=main
export DEV_REPO_BRANCH=main
```

**For k3s device-agent.sh script**

Environment file path:-
[For k3s Device's Workload Fleet Management Client-Env file](../pipeline/device-agent_k3s.env)

Update the following variables:
```bash
export GITHUB_USER=<your-github-username>
export GITHUB_TOKEN=<your-github-personal-access-token>
export DEVICE_TYPE="k3s" #Options: "k3s" or "docker", Use device-type carefully when running this script based on device
export DEV_REPO_BRANCH=main
export WFM_IP=<wfm-machine-ip>
export EXPOSED_HARBOR_IP=<wfm-machine-ip>
```

**For docker device-agent.sh script**

Environment file path:-
[For docker Device's Workload Fleet Management Client-Env file](../pipeline/device-agent_docker.env)

Update the following variables:
```bash
export GITHUB_USER=<your-github-username>
export GITHUB_TOKEN=<your-github-personal-access-token>
export DEVICE_TYPE="docker" #Options: "k3s" or "docker", Use device-type carefully when running this script based on device
export DEV_REPO_BRANCH=main
export WFM_IP=<wfm-machine-ip>
export EXPOSED_HARBOR_IP=<wfm-machine-ip>
```