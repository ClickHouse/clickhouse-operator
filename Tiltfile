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
    labels=["makefile"],
)

docker_build('clickhouse.com/clickhouse-operator', '.',
    dockerfile='Dockerfile',
    ignore=[
        'config',
        'test',
        'bin',
        '*/*/*test*',
        '*/*/*/*test*',
    ],
)

# Deploy crd & operator
k8s_yaml(kustomize('config/default'))

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

# Deploy test cluster
test_app = k8s_yaml('config/samples/v1alpha1_keepercluster.yaml')
k8s_resource(
    new_name='keeper-cluster',
    objects=['test-keeper-cluster:KeeperCluster:default'],
    labels=['test'],
)
