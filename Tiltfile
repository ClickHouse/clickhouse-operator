load('ext://cert_manager', 'deploy_cert_manager')
load("ext://restart_process", "docker_build_with_restart")

enable_cert_manager=False
enable_prometheus=False
deploy_source='kustomize'
secure_clusters = True

if enable_cert_manager:
    deploy_cert_manager(version='v1.18.2')

if enable_prometheus:
    prometheus_operator_url = "https://github.com/prometheus-operator/prometheus-operator/releases/download/v0.87.0/bundle.yaml"
    manifests = local("curl -Lq " + prometheus_operator_url, quiet=True, echo_off=True)
    k8s_yaml(manifests)
    k8s_yaml("examples/prometheus_secure_metrics_scraper.yaml")

local_resource(
    "generate",
    cmd="make manifests && make generate",
    deps=['api/', 'internal/controller'],
    ignore=[
        'api/*/*generated*',
        '*/*/*test*',
        '*/*/*/*test*',
    ],
    auto_init=False,
    labels=["operator"],
)

local_resource(
    "go-compile",
    "make build-linux-manager",
    deps=['api/', 'cmd/','internal/'],
    ignore=[
        '*/*/*test*',
        '*/*/*/*test*',
    ],
    labels=['operator'],
    resource_deps=[
        'generate',
    ],

    auto_init = False,
    trigger_mode = TRIGGER_MODE_AUTO,
)

docker_build_with_restart(
    "clickhouse.com/clickhouse-operator",
    ".",
    dockerfile = "./Dockerfile.tilt",
    entrypoint = ["/manager"],
    only = [
        "bin/manager_linux",
    ],
    live_update = [
        sync("bin/manager_linux", "/manager"),
    ],
)

if deploy_source == 'helm':
    k8s_yaml(
        helm('dist/chart', set=[
            "manager.container.image.repository=clickhouse.com/clickhouse-operator",
            "manager.container.image.label=latest",
            "manager.securityContext=null",
            "manager.manager.container.securityContext=null",
        ]),
    )
else:
    k8s_yaml(kustomize('config/tilt'))

k8s_resource(
    new_name='operator-deployment',
    workload='clickhouse-operator-controller-manager',
    labels=['operator'],
    resource_deps=[
        'generate',
    ],
)

if secure_clusters:
    k8s_yaml('examples/cluster_with_ssl.yaml')
else:
    k8s_yaml('examples/minimal.yaml')

k8s_resource(
    new_name='keeper',
    objects=['sample:KeeperCluster:default'],
    labels=['test'],
)
k8s_resource(
    new_name='clickhouse',
    objects=['sample:ClickHouseCluster:default'],
    labels=['test'],
)
