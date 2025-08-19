load('ext://cert_manager', 'deploy_cert_manager')
load("ext://restart_process", "docker_build_with_restart")

# deploy_cert_manager(version='v1.18.2')

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
    deps=['api/', 'cmd/','internal/controller'],
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

# Deploy crd & operator
k8s_yaml(kustomize('config/tilt'))

k8s_resource(
    new_name='operator-resources',
    labels=['operator'],
    resource_deps=['generate'],
    objects=[
        "clickhouse-operator-system:Namespace:default",
        "keeperclusters.clickhouse.com:CustomResourceDefinition:default",
        "clickhouseclusters.clickhouse.com:CustomResourceDefinition:default",
        "clickhouse-operator-controller-manager:ServiceAccount:clickhouse-operator-system",
        "clickhouse-operator-leader-election-role:Role:clickhouse-operator-system",
        "clickhouse-operator-keepercluster-editor-role:ClusterRole:default",
        "clickhouse-operator-keepercluster-viewer-role:ClusterRole:default",
        "clickhouse-operator-clickhousecluster-admin-role:ClusterRole",
        "clickhouse-operator-clickhousecluster-editor-role:ClusterRole",
        "clickhouse-operator-clickhousecluster-viewer-role:ClusterRole",
        "clickhouse-operator-manager-role:ClusterRole:default",
        "clickhouse-operator-metrics-auth-role:ClusterRole:default",
        "clickhouse-operator-metrics-reader:ClusterRole:default",
        "clickhouse-operator-leader-election-rolebinding:RoleBinding:clickhouse-operator-system",
        "clickhouse-operator-manager-rolebinding:ClusterRoleBinding:default",
        "clickhouse-operator-metrics-auth-rolebinding:ClusterRoleBinding:default",
        "clickhouse-operator-serving-cert:Certificate:clickhouse-operator-system",
        "clickhouse-operator-metrics-certs:Certificate:clickhouse-operator-system",
        "clickhouse-operator-selfsigned-issuer:Issuer:clickhouse-operator-system",
        "clickhouse-operator-mutating-webhook-configuration:MutatingWebhookConfiguration:default",
        "clickhouse-operator-validating-webhook-configuration:ValidatingWebhookConfiguration",
    ],
)

k8s_resource(
    new_name='operator-deployment',
    workload='clickhouse-operator-controller-manager',
    labels=['operator'],
    resource_deps=[
        'operator-resources',
        'generate',
    ],
)

secure = True
if secure:
    k8s_yaml('examples/cluster_with_ssl.yaml')
    k8s_resource(
        new_name='certs',
        objects=[
            'selfsigned-issuer:ClusterIssuer:default',
            'ca-cert:Certificate:default',
            'local-issuer:Issuer:default',
            'keeper-cert:Certificate:default',
            'clickhouse-cert:Certificate:default',
        ],
        labels=['test'],
    )
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
