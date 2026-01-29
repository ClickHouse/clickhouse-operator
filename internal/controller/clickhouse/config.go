package clickhouse

import (
	_ "embed"
	"errors"
	"fmt"
	"path"
	"strings"
	"text/template"

	"gopkg.in/yaml.v2"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controller"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/keeper"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
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

	generators []configGenerator
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
			"indent": func(countRaw any, strRaw any) (string, error) {
				count, ok := countRaw.(int)
				if !ok {
					return "", fmt.Errorf("indent: expected int for indentation value, got %T", countRaw)
				}

				str, ok := strRaw.(string)
				if !ok {
					return "", fmt.Errorf("indent: expected string for content value, got %T", strRaw)
				}

				builder := strings.Builder{}
				indentation := strings.Repeat(" ", count)

				for _, line := range strings.Split(str, "\n") {
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

	generators = append(generators,
		&extraConfigGenerator{
			Name:          ExtraConfigFileName,
			ConfigSubPath: ConfigDPath,
			Getter: func(r *clickhouseReconciler) []byte {
				return r.Cluster.Spec.Settings.ExtraConfig.Raw
			},
		},
		&extraConfigGenerator{
			Name:          ExtraUsersConfigFileName,
			ConfigSubPath: UsersDPath,
			Getter: func(r *clickhouseReconciler) []byte {
				return r.Cluster.Spec.Settings.ExtraUsersConfig.Raw
			},
		})
}

type configGenerator interface {
	Filename() string
	Path() string
	ConfigKey() string
	Exists(r *clickhouseReconciler) bool
	Generate(r *clickhouseReconciler, id v1.ClickHouseReplicaID) (string, error)
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
	return controllerutil.PathToName(path.Join(g.path, g.filename))
}

func (g *templateConfigGenerator) Exists(*clickhouseReconciler) bool {
	return true
}

func (g *templateConfigGenerator) Generate(r *clickhouseReconciler, id v1.ClickHouseReplicaID) (string, error) {
	data, err := g.generator(g.template, r, id)
	if err != nil {
		return "", fmt.Errorf("generate config %s: %w", g.filename, err)
	}

	return data, nil
}

type configGeneratorFunc func(tmpl *template.Template, r *clickhouseReconciler, id v1.ClickHouseReplicaID) (string, error)

type baseConfigParams struct {
	Path   string
	Log    controller.LoggerConfig
	Macros map[string]any

	KeeperNodes               []keeperNode
	KeeperIdentityEnv         string
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

func baseConfigGenerator(tmpl *template.Template, r *clickhouseReconciler, id v1.ClickHouseReplicaID) (string, error) {
	keeperNodes := make([]keeperNode, 0, r.keeper.Replicas())
	for _, host := range r.keeper.Hostnames() {
		if r.keeper.Spec.Settings.TLS.Enabled {
			keeperNodes = append(keeperNodes, keeperNode{
				Host:   host,
				Port:   keeper.PortNativeSecure,
				Secure: true,
			})
		} else {
			keeperNodes = append(keeperNodes, keeperNode{
				Host: host,
				Port: keeper.PortNative,
			})
		}
	}

	openSSL := controller.OpenSSLConfig{}
	if r.Cluster.Spec.Settings.TLS.Enabled {
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

	if r.Cluster.Spec.Settings.TLS.CABundle != nil {
		openSSL.Client.CAConfig = path.Join(TLSConfigPath, CustomCAFilename)
		openSSL.Client.VerificationMode = "relaxed"
		openSSL.Client.DisableProtocols = "sslv2,sslv3"
		openSSL.Client.PreferServerCiphers = true
	}

	params := baseConfigParams{
		Path: BaseDataPath,
		Log:  controller.GenerateLoggerConfig(r.Cluster.Spec.Settings.Logger, LogPath, "clickhouse-server"),
		Macros: map[string]any{
			"cluster": DefaultClusterName,
			"shard":   id.ShardID,
			"replica": id.Index,
		},

		KeeperNodes:               keeperNodes,
		KeeperIdentityEnv:         EnvKeeperIdentity,
		DistributedDDLPath:        KeeperPathDistributedDDL,
		DistributedDDLProfileName: DefaultProfileName,
		UsersXMLPath:              UsersFileName,
		UsersZookeeperPath:        KeeperPathUsers,
		UDFZookeeperPath:          KeeperPathUDF,

		OpenSSL: openSSL,
	}

	builder := strings.Builder{}
	if err := tmpl.Execute(&builder, params); err != nil {
		return "", fmt.Errorf("template base config: %w", err)
	}

	return builder.String(), nil
}

type networkConfigParams struct {
	InterserverHTTPPort           uint16
	InterserverHTTPUser           string
	InterserverHTTPPasswordEnvVar string
	ManagementPort                uint16
	Protocols                     map[string]protocol
}

func networkConfigGenerator(tmpl *template.Template, r *clickhouseReconciler, _ v1.ClickHouseReplicaID) (string, error) {
	protocols := buildProtocols(r.Cluster)
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
		return "", fmt.Errorf("template network config: %w", err)
	}

	return builder.String(), nil
}

func logTablesConfigGenerator(tmpl *template.Template, _ *clickhouseReconciler, _ v1.ClickHouseReplicaID) (string, error) {
	builder := strings.Builder{}
	if err := tmpl.Execute(&builder, nil); err != nil {
		return "", fmt.Errorf("template log tables: %w", err)
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

func userConfigGenerator(tmpl *template.Template, r *clickhouseReconciler, _ v1.ClickHouseReplicaID) (string, error) {
	passEnv := EnvDefaultUserPassword

	passType := ""
	if r.Cluster.Spec.Settings.DefaultUserPassword == nil {
		passEnv = ""
	} else {
		passType = r.Cluster.Spec.Settings.DefaultUserPassword.PasswordType
	}

	params := userConfigParams{
		DefaultUserPasswordEnv:   passEnv,
		DefaultUserType:          passType,
		DefaultProfileName:       DefaultProfileName,
		OperatorUserName:         OperatorManagementUsername,
		OperatorUserPasswordHash: controllerutil.Sha256Hash(r.secret.Data[SecretKeyManagementPassword]),
	}

	builder := strings.Builder{}
	if err := tmpl.Execute(&builder, params); err != nil {
		return "", fmt.Errorf("template user config: %w", err)
	}

	return builder.String(), nil
}

type clientConfigParams struct {
	ManagementPort         uint16
	DefaultUserPasswordEnv string
}

func clientConfigGenerator(tmpl *template.Template, r *clickhouseReconciler, _ v1.ClickHouseReplicaID) (string, error) {
	passEnv := EnvDefaultUserPassword
	if r.Cluster.Spec.Settings.DefaultUserPassword == nil {
		passEnv = ""
	}

	params := clientConfigParams{
		ManagementPort:         PortManagement,
		DefaultUserPasswordEnv: passEnv,
	}

	builder := strings.Builder{}
	if err := tmpl.Execute(&builder, params); err != nil {
		return "", fmt.Errorf("template client config: %w", err)
	}

	return builder.String(), nil
}

type extraConfigGenerator struct {
	Name          string
	ConfigSubPath string
	Getter        func(r *clickhouseReconciler) []byte
}

func (g *extraConfigGenerator) Filename() string {
	return g.Name
}

func (g *extraConfigGenerator) Path() string {
	return path.Join(ConfigPath, g.ConfigSubPath)
}

func (g *extraConfigGenerator) ConfigKey() string {
	return g.Name
}

func (g *extraConfigGenerator) Exists(r *clickhouseReconciler) bool {
	return len(g.Getter(r)) > 0
}

func (g *extraConfigGenerator) Generate(r *clickhouseReconciler, _ v1.ClickHouseReplicaID) (string, error) {
	if !g.Exists(r) {
		return "", errors.New("extra config generator called, but no extra config provided")
	}

	return string(g.Getter(r)), nil
}
