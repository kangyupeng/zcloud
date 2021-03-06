package k8s

import (
	"cloud/sql"
	"strings"
	"cloud/util"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"github.com/astaxie/beego/logs"
	"strconv"
	"k8s.io/client-go/kubernetes"
	v12 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/integer"
	"golang.org/x/crypto/openpgp/errors"
)

const (
	nginxLbNamespace = "lb--nginx"
	nginxSslTemplate = `server {
          listen 443 ssl;
          listen 80;
          access_log  logs/DOMAIN_access.log;
          error_log   logs/DOMAIN_error.log;
          server_name DOMAIN;
          ssl_protocols  SSLv2 SSLv3 TLSv1;
          ssl_ciphers  HIGH:!aNULL:!MD5;
          ssl_prefer_server_ciphers on;
          ssl_certificate vhosts/ssl/CERTKEY.pem;
          ssl_certificate_key vhosts/ssl/CERTKEY.key;

          location / {
                proxy_pass http://DOMAIN;
          }
}`

	nginxTemplate = `server {
          listen      80;
          access_log  logs/DOMAIN_access.log;
          error_log   logs/DOMAIN_error.log;
          server_name DOMAIN;

          location / {
                proxy_pass http://DOMAIN;
          }
}`
	upstreamTemplate = `
upstream DOMAIN {
POD
}
`
	selectLbDomainSuffix   = "select lb_domain_suffix as domain from cloud_lb"
	InsertCloudLbNginxConf = "insert into cloud_lb_nginx_conf"
	SelectCloudLbNginxConf = "select cert_file,conf_id,service_id,create_user,lb_service_id,resource_name,app_name,cluster_name,last_modify_time,last_modify_user,domain,vhost,create_time,service_name from cloud_lb_nginx_conf"
	SelectCloudLbCert      = "select pem_value,last_modify_time,last_modify_user,description,cert_id,create_time,create_user,cert_key,cert_value from cloud_lb_cert"
	SelectCloudLbService   = "select lb_method,flow_service_name,service_version,percent,resource_name,service_id,default_domain,lb_id,service_id,lb_type,app_name,domain,cluster_name,last_modify_user,service_id,last_modify_time,create_time,create_user,service_name,lb_name,cert_file,description,listen_port,container_port from cloud_lb_service"
)

// 获取配置了lb的数据
// 2018-02-01 11:43
func getLbServiceData() []CloudLbService {
	data := []CloudLbService{}
	q := sql.SearchSql(CloudLbService{}, SelectCloudLbService, sql.SearchMap{})
	sql.Raw(q).QueryRows(&data)
	return data
}

// 创建nginx配置信息
// 2018-02-03 07:48
func makeNgxinConfMap(clustername string, nginxConfigMap []ConfigureData) {
	serviceParam := ServiceParam{}
	clientset, _ := GetClient(clustername)
	cl2, _ := GetYamlClient(clustername, "", "v1", "api")
	serviceParam.Cl2 = cl2
	serviceParam.Cl3 = clientset
	serviceParam.ConfigureData = nginxConfigMap
	serviceParam.Namespace = nginxLbNamespace
	CreateConfigmap(serviceParam)
}

// 2018-02-03 07:51
// 创建用于测试的nginx配置
func MakeTestNginxConfMap(confdata map[string]interface{}, ssldata map[string]interface{}, clustername string) {
	nginxConfigMap := []ConfigureData{}
	nginxConfigMap = append(nginxConfigMap, getNgxinDefaulgConfig(
		NginxConfigPath,
		LbNginxConfig,
		confdata,
		"-test"))
	nginxConfigMap = append(nginxConfigMap, getNgxinDefaulgConfig(
		NginxSslPath,
		LbNginxSsl,
		ssldata,
		"-test"))
	logs.Info("MakeTestNginxConfMap", nginxConfigMap)
	makeNgxinConfMap(clustername, nginxConfigMap)
}

// 2018-02-01 14:28
// 创建nginx配置
func makeNginxConfigMap(confdata map[string]interface{}, upstreamdata map[string]interface{}, ssldata map[string]interface{}, clustername string, conftype string) {
	nginxConfigMap := []ConfigureData{}

	nginxConfigMap = append(nginxConfigMap, getNgxinDefaulgConfig(
		NginxConfigPath,
		LbNginxConfig,
		confdata,
		conftype))
	nginxConfigMap = append(nginxConfigMap, getNgxinDefaulgConfig(
		NginxUpstreamPath,
		LbNginxUpstream,
		upstreamdata,
		conftype))
	nginxConfigMap = append(nginxConfigMap, getNgxinDefaulgConfig(
		NginxSslPath,
		LbNginxSsl,
		ssldata,
		conftype))

	makeNgxinConfMap(clustername, nginxConfigMap)
}

// 2018-02-02 09:03
// 获取pod类型的upstream
func makePodUpstream(client kubernetes.Clientset, servicename string, namespace string) []string {
	endpoint, err := client.CoreV1().Endpoints(namespace).Get(servicename, v1.GetOptions{})
	ips := make([]string, 0)
	if err != nil {
		logs.Error("获取Endpoints失败", err)
		return ips
	}

	for _, sets := range endpoint.Subsets {
		for _, add := range sets.Addresses {
			if len(sets.Ports) > 0 {
				port := strconv.Itoa(int(sets.Ports[0].Port))
				ips = append(ips, "    server "+add.IP+":"+port+" max_fails=8 fail_timeout=3s;")
			}
		}
	}
	return ips
}

// 2018-02-02 08:22
// 生成node节点方式的upstream
func makeNodeUpstream(clientset kubernetes.Clientset, svcport string, ips []string) []string {
	nodes := GetNodes(clientset, "lb=nginx")
	if len(nodes) == 0 {
		nodes = GetNodes(clientset, "")
	}
	for _, v := range nodes {
		for _, c := range v.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ips = append(ips, "    server "+v.Name+":"+svcport+";")
			}
		}
	}
	return ips
}

// 2108-02-03 11:30
// 获取虚拟主机证书文件
func GetCertConfigData(keyfile string, sslDbName map[string]interface{}) map[string]interface{} {
	certData := selectCertfile(keyfile)
	// 配置证书信息 私钥
	sslDbName[keyfile+".key"] = certData.CertValue
	// 公钥
	sslDbName[keyfile+".pem"] = certData.PemValue
	return sslDbName
}

// 2018-02-16 14:36
// 获取服务名字
func getServiceName(v CloudLbService) string {
	// 通过pod模式负载
	serviceName := v.ServiceName
	if v.ServiceVersion != "" {
		serviceName = util.Namespace(serviceName, v.ServiceVersion)
	} else {
		serviceName = util.Namespace(serviceName, "1")
	}
	return serviceName
}

// 2018-02-16 14:43
// 获取服务的ip地址和端口
func getServiceIps(client kubernetes.Clientset, svc v12.Service, ips []string) []string {
	if len(svc.Spec.Ports) > 0 {
		port := strconv.Itoa(int(svc.Spec.Ports[0].NodePort))
		ips = makeNodeUpstream(client, port, ips)
	}
	return ips
}

// 2018-02-17 07:27
// 按百分比计算
// 计算切入流量的服务器
func getFlowTempIps(tempIps []string, percent int, ips []string) []string {
	length := len(tempIps)
	if length == 0 {
		return ips
	}
	p := float64(length) * (float64(percent) / 100)
	if p < 1 && length > 1 {
		p = 1
	}
	end := integer.RoundToInt32(p)
	if end >= int32(length) {
		end = end - 1
	}
	tempIps = tempIps[0: int(end)]
	ips = append(ips, tempIps...)
	logs.Info(p, length, end, tempIps, ips)
	return ips
}

// 2018-02-16 14:61
// 获取流量切入服务ip和端口
func getFlowServicePort(v CloudLbService, client kubernetes.Clientset, ips []string) []string {
	percent := v.Percent
	logs.Info("获取流量切入", percent)
	if percent > 0 {
		namespace := util.Namespace(v.AppName, v.ResourceName)
		svc := GetServicePort(client, namespace, v.FlowServiceName)
		tempIps := getServiceIps(client, *svc, ips)
		logs.Info("获取切入流量", namespace, v.FlowServiceName,tempIps)
		ips = getFlowTempIps(tempIps, percent, ips)
	}
	return ips
}

// 2018-02-17 20:58
// 更新nginx的upstream
func getLbNginxUpstream(client kubernetes.Clientset) (bool, map[string]string) {
	cm, err := client.CoreV1().ConfigMaps(nginxLbNamespace).Get("lb-nginx-upstream", v1.GetOptions{})
	if err == nil {
		return true, cm.Data
	}
	return false, make(map[string]string)
}

// 2018-02-17 21:10
// 更新nginx的upstream
func UpdateNginxLbUpstream(param UpdateLbNginxUpstream) error{
	cl, err := GetClient(param.ClusterName)
	if err != nil {
		logs.Error("获取k8s客户端失败", err)
		return err
	}

	status, cm := getLbNginxUpstream(cl)
	if !status{
		return errors.ErrUnknownIssuer
	}

	ips := make([]string, 0)
	//  切入流量的地址
	ips = getFlowServicePort(param.V, cl, ips)
	svc := GetServicePort(cl, param.Namespace, util.Namespace(param.V.ServiceName, param.V.ServiceVersion))
	ips = getServiceIps(cl, *svc, ips)

	upstreamTemp := strings.Replace(upstreamTemplate, "DOMAIN", param.Domain, -1)
	upstreamTemp = strings.Replace(upstreamTemp, "POD", strings.Join(ips, "\n"), -1)
	cm[param.Domain+".upstream"] = upstreamTemp

	nginxConfigMap := []ConfigureData{}

	confData := make(map[string]interface{})
	for k, v := range cm {
		confData[k] = v
	}

	nginxConfigMap = append(nginxConfigMap, getNgxinDefaulgConfig(
		NginxUpstreamPath,
		LbNginxUpstream,
		confData,
		""))
	makeNgxinConfMap(param.ClusterName, nginxConfigMap)
	return nil
}

// 生成nginx配置文件
func CreateNginxConf(conftype string) {
	configDbName := make(map[string]interface{})
	upstreamDbName := make(map[string]interface{})
	sslDbName := make(map[string]interface{})
	nginxMap := selectNginxConfFromDb()

	data := getLbServiceData()
	clusters := getClusters(data)

	certLock := util.Lock{}

	for _, cluster := range clusters {
		var upstreamTemp string
		var vhosstTemp string
		client, _ := GetClient(cluster)

		for _, v := range data {
			if v.ClusterName != cluster {
				continue
			}

			upstreamTemp = strings.Replace(upstreamTemplate, "DOMAIN", v.Domain, -1)
			key := v.ClusterName + v.Domain

			vhosstTemp = strings.Replace(nginxTemplate, "DOMAIN", v.Domain, -1)
			if _, ok := nginxMap.Get(key); ok {
				vhosstTemp = nginxMap.GetVString(key)
			}

			namespace := util.Namespace(v.AppName, v.ResourceName)
			ips := make([]string, 0)

			// 通过pod模式负载
			serviceName := getServiceName(v)
			if v.LbMethod == "pod" {
				ips = makePodUpstream(client, serviceName, namespace)
			} else {
				//  切入流量的地址
				ips = getFlowServicePort(v, client, ips)
				svc := GetServicePort(client, namespace, serviceName)
				logs.Info("获取到负载均衡版本号", serviceName, util.ObjToString(svc))
				ips = getServiceIps(client, *svc, ips)
			}
			logs.Info("获取到IPs", util.ObjToString(ips))
			if len(ips) == 0 {
				logs.Info("没有获取到可用地址", v.ServiceName, v.ResourceName)
				continue
			}

			upstreamTemp = strings.Replace(upstreamTemp, "POD", strings.Join(ips, "\n"), -1)
			upstreamDbName[v.Domain+".upstream"] = upstreamTemp

			if v.CertFile != "" && v.CertFile != "0" {
				if _, ok := certLock.Get(v.CertFile); ! ok {
					certLock.Put(v.CertFile, "1")
					// 公钥
					sslDbName = GetCertConfigData(v.CertFile, sslDbName)
					logs.Info("开始获取证书配置", sslDbName)

					if _, ok := nginxMap.Get(key); !ok {
						vhosstTemp = strings.Replace(nginxSslTemplate, "DOMAIN", v.Domain, -1)
						vhosstTemp = strings.Replace(vhosstTemp, "CERTKEY", v.CertFile, -1)
					}
				}
			}

			configDbName[v.Domain+".conf"] = vhosstTemp
			writeNginxConfToDb(v, nginxMap, vhosstTemp)
		}
		makeNginxConfigMap(configDbName, upstreamDbName, sslDbName, cluster, conftype)
	}
}

// 将生成的nginx配置数据写入到数据中，方便用户修改
// 2018-02-01 13:33
func writeNginxConfToDb(lb CloudLbService, nginxMap util.Lock, vhost string) {
	if _, ok := nginxMap.Get(lb.ClusterName + lb.Domain); ok {
		return
	}
	conf := CloudLbNginxConf{
		ServiceName:    lb.ServiceName,
		AppName:        lb.AppName,
		ClusterName:    lb.ClusterName,
		CreateTime:     util.GetDate(),
		Domain:         lb.Domain,
		ResourceName:   lb.ResourceName,
		LastModifyTime: util.GetDate(),
		Vhost:          vhost,
		ServiceId:      lb.ServiceId,
		LbServiceId:    lb.LbServiceId,
		CreateUser:     lb.CreateUser,
		CertFile:       lb.CertFile,
	}
	conf.LbServiceId = strconv.FormatInt(lb.ServiceId, 10)
	q := sql.InsertSql(conf, InsertCloudLbNginxConf)
	sql.Raw(q).Exec()
}

// 查询nginx配置数据到map
// 用来做插入判断,不用每次都查
// 2018-02-01 13:41
func selectNginxConfFromDb() util.Lock {
	result := util.Lock{}
	data := []CloudLbNginxConf{}
	q := sql.SearchSql(CloudLbNginxConf{}, SelectCloudLbNginxConf, sql.SearchMap{})
	sql.Raw(q).QueryRows(&data)
	for _, v := range data {
		result.Put(v.ClusterName+v.Domain, v.Vhost)
	}
	return result
}

// 2018-02-01 14:25
// 创建nginx配置,按不同集群创建
func getClusters(data []CloudLbService) []string {
	result := make([]string, 0)
	for _, v := range data {
		result = append(result, v.ClusterName)
	}
	return result
}

// 2018-02-01 17:47
// 查询负载机器的域名后缀
func getLbDetail(lbname string, clustername string) CloudLbService {
	data := CloudLbService{}
	searchMap := sql.GetSearchMapV("ClusterName", clustername, "LbName", lbname)
	q := sql.SearchSql(data, selectLbDomainSuffix, searchMap)
	sql.Raw(q).QueryRow(&data)
	return data
}

// 2018-02-02 16:00
// 查询证书配置
func selectCertfile(name string) CloudLbCert {
	data := CloudLbCert{}
	searchMap := sql.GetSearchMapV("CertKey", name)
	q := sql.SearchSql(data, SelectCloudLbCert, searchMap)
	sql.Raw(q).QueryRow(&data)
	return data
}
