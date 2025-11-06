package clickhouse

import (
	_ "embed"
	"fmt"
	"path"
	"strings"
	"text/template"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/controller"
	keepercontroller "github.com/clickhouse-operator/internal/controller/keeper"
	"github.com/clickhouse-operator/internal/util"
	"gopkg.in/yaml.v2"
)

var (
	//go:embed templates/base.yaml.tmpl
	baseConfigTemplateStr string
	//go:embed templates/network.yaml.tmpl
	networkConfigTemplateStr string
	//go:embed templates/log_tables.yaml.tmpl
	logTablesConfigTemplateStr string
	//go:embed templates/users.yaml.tmpl
	userConfigTemplateStr string
	//go:embed templates/client.yaml.tmpl
	clientConfigTemplateStr string

	generators []ConfigGenerator
)

func init() {
	for _, templateSpec := range []struct {
		Path      string
		Filename  string
		Raw       string
		Generator configGeneratorFunc
	}{{
		Path:      ConfigPath,
		Filename:  ConfigFileName,
		Raw:       baseConfigTemplateStr,
		Generator: baseConfigGenerator,
	}, {
		Path:      path.Join(ConfigPath, ConfigDPath),
		Filename:  "00-network.yaml",
		Raw:       networkConfigTemplateStr,
		Generator: networkConfigGenerator,
	}, {
		Path:      path.Join(ConfigPath, ConfigDPath),
		Filename:  "00-logs-tables.yaml",
		Raw:       logTablesConfigTemplateStr,
		Generator: logTablesConfigGenerator,
	}, {
		Path:      ConfigPath,
		Filename:  UsersFileName,
		Raw:       userConfigTemplateStr,
		Generator: userConfigGenerator,
	}, {
		Path:      ClientConfigPath,
		Filename:  ClientConfigFileName,
		Raw:       clientConfigTemplateStr,
		Generator: clientConfigGenerator,
	}} {
		tmpl := template.New("").Funcs(template.FuncMap{
			"yaml": func(v any) (string, error) {
				data, err := yaml.Marshal(v)
				return string(data), err
			},
			"indent": func(val any, v any) (string, error) {
				builder := strings.Builder{}
				indentation := strings.Repeat(" ", val.(int))
				for _, line := range strings.Split(v.(string), "\n") {
					if _, err := builder.WriteString(fmt.Sprintf("%s%s\n", indentation, line)); err != nil {
						return "", fmt.Errorf("failed to write indented line: %w", err)
					}
				}

				return builder.String(), nil
			},
		})
		if _, err := tmpl.Parse(templateSpec.Raw); err != nil {
			panic(fmt.Sprintf("failed to parse template %s: %v", templateSpec.Filename, err))
		}

		generators = append(generators, &templateConfigGenerator{
			filename:  templateSpec.Filename,
			path:      templateSpec.Path,
			template:  tmpl,
			generator: templateSpec.Generator,
		})
	}

	generators = append(generators, &extraConfigGenerator{})
}

type ConfigGenerator interface {
	Filename() string
	Path() string
	ConfigKey() string
	Exists(ctx *reconcileContext) bool
	Generate(ctx *reconcileContext, id v1.ReplicaID) (string, error)
}

type templateConfigGenerator struct {
	filename  string
	path      string
	template  *template.Template
	generator configGeneratorFunc
}

func (g *templateConfigGenerator) Filename() string {
	return g.filename
}

func (g *templateConfigGenerator) Path() string {
	return g.path
}

func (g *templateConfigGenerator) ConfigKey() string {
	return util.PathToName(path.Join(g.path, g.filename))
}

func (g *templateConfigGenerator) Exists(*reconcileContext) bool {
	return true
}

func (g *templateConfigGenerator) Generate(ctx *reconcileContext, id v1.ReplicaID) (string, error) {
	data, err := g.generator(g.template, ctx, id)
	if err != nil {
		return "", fmt.Errorf("generate config %s: %w", g.filename, err)
	}

	return data, nil
}

type configGeneratorFunc func(tmpl *template.Template, ctx *reconcileContext, id v1.ReplicaID) (string, error)

type baseConfigParams struct {
	Path   string
	Log    controller.LoggerConfig
	Macros map[string]any

	KeeperNodes               []keeperNode
	KeeperIdentityEnv         string
	ClusterDiscoveryPath      string
	ShardID                   int32
	DistributedDDLPath        string
	DistributedDDLProfileName string
	UsersXMLPath              string
	UsersZookeeperPath        string
	UDFZookeeperPath          string

	OpenSSL controller.OpenSSLConfig
}

type keeperNode struct {
	Host   string
	Port   int32
	Secure bool
}

func baseConfigGenerator(tmpl *template.Template, ctx *reconcileContext, id v1.ReplicaID) (string, error) {
	keeperNodes := make([]keeperNode, 0, ctx.keeper.Replicas())
	for _, host := range ctx.keeper.Hostnames() {
		if ctx.keeper.Spec.Settings.TLS.Enabled {
			keeperNodes = append(keeperNodes, keeperNode{
				Host:   host,
				Port:   keepercontroller.PortNativeSecure,
				Secure: true,
			})
		} else {
			keeperNodes = append(keeperNodes, keeperNode{
				Host: host,
				Port: keepercontroller.PortNative,
			})
		}
	}

	openSSL := controller.OpenSSLConfig{}
	if ctx.Cluster.Spec.Settings.TLS.Enabled {
		params := controller.OpenSSLParams{
			CertificateFile:     path.Join(TLSConfigPath, CertificateFilename),
			PrivateKeyFile:      path.Join(TLSConfigPath, KeyFilename),
			CAConfig:            path.Join(TLSConfigPath, CABundleFilename),
			VerificationMode:    "relaxed",
			DisableProtocols:    "sslv2,sslv3",
			PreferServerCiphers: true,
		}

		openSSL = controller.OpenSSLConfig{
			Server: params,
			Client: params,
		}
	}

	if ctx.Cluster.Spec.Settings.TLS.CABundle != nil {
		openSSL.Client.CAConfig = path.Join(TLSConfigPath, CustomCAFilename)
		openSSL.Client.VerificationMode = "relaxed"
		openSSL.Client.DisableProtocols = "sslv2,sslv3"
		openSSL.Client.PreferServerCiphers = true
	}

	params := baseConfigParams{
		Path: BaseDataPath,
		Log:  controller.GenerateLoggerConfig(ctx.Cluster.Spec.Settings.Logger, LogPath, "clickhouse-server"),
		Macros: map[string]any{
			"cluster": DefaultClusterName,
			"shard":   id.ShardID,
			"replica": id.Index,
		},

		KeeperNodes:               keeperNodes,
		KeeperIdentityEnv:         EnvKeeperIdentity,
		ClusterDiscoveryPath:      KeeperPathDiscovery,
		ShardID:                   id.ShardID,
		DistributedDDLPath:        KeeperPathDistributedDDL,
		DistributedDDLProfileName: DefaultProfileName,
		UsersXMLPath:              UsersFileName,
		UsersZookeeperPath:        KeeperPathUsers,
		UDFZookeeperPath:          KeeperPathUDF,

		OpenSSL: openSSL,
	}

	builder := strings.Builder{}
	if err := tmpl.Execute(&builder, params); err != nil {
		return "", err
	}

	return builder.String(), nil
}

type networkConfigParams struct {
	InterserverHTTPPort           uint16
	InterserverHTTPUser           string
	InterserverHTTPPasswordEnvVar string
	ManagementPort                uint16
	Protocols                     map[string]Protocol
}

func networkConfigGenerator(tmpl *template.Template, ctx *reconcileContext, _ v1.ReplicaID) (string, error) {
	protocols := buildProtocols(ctx.Cluster)
	delete(protocols, "interserver")
	delete(protocols, "management")

	params := networkConfigParams{
		InterserverHTTPPort:           PortInterserver,
		InterserverHTTPUser:           InterserverUserName,
		InterserverHTTPPasswordEnvVar: EnvInterserverPassword,
		ManagementPort:                PortManagement,
		Protocols:                     protocols,
	}

	builder := strings.Builder{}
	if err := tmpl.Execute(&builder, params); err != nil {
		return "", err
	}

	return builder.String(), nil
}

func logTablesConfigGenerator(tmpl *template.Template, _ *reconcileContext, _ v1.ReplicaID) (string, error) {
	builder := strings.Builder{}
	if err := tmpl.Execute(&builder, nil); err != nil {
		return "", err
	}

	return builder.String(), nil
}

type userConfigParams struct {
	DefaultUserPasswordEnv   string
	DefaultUserType          string
	DefaultProfileName       string
	OperatorUserName         string
	OperatorUserPasswordHash string
}

func userConfigGenerator(tmpl *template.Template, ctx *reconcileContext, _ v1.ReplicaID) (string, error) {
	passEnv := EnvDefaultUserPassword
	passType := ""
	if ctx.Cluster.Spec.Settings.DefaultUserPassword == nil {
		passEnv = ""
	} else {
		passType = ctx.Cluster.Spec.Settings.DefaultUserPassword.PasswordType
	}

	params := userConfigParams{
		DefaultUserPasswordEnv:   passEnv,
		DefaultUserType:          passType,
		DefaultProfileName:       DefaultProfileName,
		OperatorUserName:         OperatorManagementUsername,
		OperatorUserPasswordHash: util.Sha256Hash(ctx.secret.Data[SecretKeyManagementPassword]),
	}

	builder := strings.Builder{}
	if err := tmpl.Execute(&builder, params); err != nil {
		return "", err
	}

	return builder.String(), nil
}

type clientConfigParams struct {
	ManagementPort         uint16
	DefaultUserPasswordEnv string
}

func clientConfigGenerator(tmpl *template.Template, ctx *reconcileContext, _ v1.ReplicaID) (string, error) {
	passEnv := EnvDefaultUserPassword
	if ctx.Cluster.Spec.Settings.DefaultUserPassword == nil {
		passEnv = ""
	}

	params := clientConfigParams{
		ManagementPort:         PortManagement,
		DefaultUserPasswordEnv: passEnv,
	}

	builder := strings.Builder{}
	if err := tmpl.Execute(&builder, params); err != nil {
		return "", err
	}

	return builder.String(), nil
}

type extraConfigGenerator struct{}

func (g *extraConfigGenerator) Filename() string {
	return ExtraConfigFileName
}

func (g *extraConfigGenerator) Path() string {
	return path.Join(ConfigPath, ConfigDPath)
}

func (g *extraConfigGenerator) ConfigKey() string {
	return ExtraConfigFileName
}

func (g *extraConfigGenerator) Exists(ctx *reconcileContext) bool {
	return len(ctx.Cluster.Spec.Settings.ExtraConfig.Raw) > 0
}

func (g *extraConfigGenerator) Generate(ctx *reconcileContext, _ v1.ReplicaID) (string, error) {
	if !g.Exists(ctx) {
		return "", fmt.Errorf("extra config generator called, but no extra config provided")
	}

	return string(ctx.Cluster.Spec.Settings.ExtraConfig.Raw), nil
}
