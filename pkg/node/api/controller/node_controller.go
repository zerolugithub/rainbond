// RAINBOND, Application Management Platform
// Copyright (C) 2014-2017 Goodrain Co., Ltd.

// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. For any non-GPL usage of Rainbond,
// one or multiple Commercial Licenses authorized by Goodrain Co., Ltd.
// must be obtained first.

// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"github.com/goodrain/rainbond/pkg/node/utils"
	"github.com/Sirupsen/logrus"
	"github.com/go-chi/chi"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/version"
	"github.com/prometheus/node_exporter/collector"

	"github.com/goodrain/rainbond/pkg/node/api/model"

	httputil "github.com/goodrain/rainbond/pkg/util/http"
	"strconv"
	"github.com/goodrain/rainbond/pkg/node/core/k8s"
	"io/ioutil"
	"errors"
)

func init() {
	prometheus.MustRegister(version.NewCollector("node_exporter"))
}

//NewNode 创建一个节点
func NewNode(w http.ResponseWriter, r *http.Request) {
	var node model.APIHostNode
	if ok := httputil.ValidatorRequestStructAndErrorResponse(r, w, &node, nil); !ok {
		return
	}
	if node.Role == nil ||len(node.Role)==0{
		err:=utils.CreateAPIHandleError(400, fmt.Errorf("node role must not null"))
		err.Handle(r,w)
		return
	}
	if err := nodeService.AddNode(&node); err != nil {
		err.Handle(r, w)
		return
	}
	httputil.ReturnSuccess(r, w, nil)
}

//NewMultipleNode 多节点添加操作
func NewMultipleNode(w http.ResponseWriter, r *http.Request) {
	var nodes []model.APIHostNode
	if ok := httputil.ValidatorRequestStructAndErrorResponse(r, w, &nodes, nil); !ok {
		return
	}
	var successnodes []model.APIHostNode
	for _, node := range nodes {
		if err := nodeService.AddNode(&node); err != nil {
			continue
		}
		successnodes = append(successnodes, node)
	}
	httputil.ReturnSuccess(r, w, successnodes)
}

//GetNodes 获取全部节点
func GetNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := nodeService.GetAllNode()
	if err != nil {
		err.Handle(r, w)
		return
	}
	for _,v:=range nodes {
		handleStatus(v)
	}
	httputil.ReturnSuccess(r, w, nodes)
}

func handleStatus(v *model.HostNode){
	if v.NodeStatus!=nil{
		for _,condiction:=range v.Conditions{
			if v.Status == "unschedulable" {

			}else{
				if condiction.Type=="Ready"&&condiction.Status=="True" {
					v.Status="running"
				}
			}
		}
	}
	if v.Role.HasRule("manage") {
		if v.Alived {
			for _,condition:=range v.Conditions{
				if condition.Type=="NodeInit"&&condition.Status=="True"{
					v.Status="running"
				}
			}
		}
	}
}

//GetNode 获取一个节点详情
func GetNode(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	node, err := nodeService.GetNode(nodeID)
	if err != nil {
		err.Handle(r, w)
		return
	}
	httputil.ReturnSuccess(r, w, node)
}

//GetRuleNodes 获取分角色节点
func GetRuleNodes(w http.ResponseWriter, r *http.Request) {
	rule := chi.URLParam(r, "rule")
	if rule != "compute" && rule != "manage" && rule != "storage" {
		httputil.ReturnError(r, w, 400, rule+" rule is not define")
		return
	}
	nodes, err := nodeService.GetAllNode()
	if err != nil {
		err.Handle(r, w)
		return
	}
	var masternodes []*model.HostNode
	for _, node := range nodes {
		if node.Role.HasRule(rule) {
			masternodes = append(masternodes, node)
		}
	}
	httputil.ReturnSuccess(r, w, masternodes)
}


//UpNode 节点上线，计算节点操作
func CheckNode(w http.ResponseWriter, r *http.Request) {
	nodeUID := strings.TrimSpace(chi.URLParam(r, "node_id"))
	if len(nodeUID)==0 {
		err:=utils.APIHandleError{
			Code:404,
			Err:errors.New(fmt.Sprintf("can't find node by node_id %s",nodeUID)),
		}
		err.Handle(r,w)
		return
	}
	tasks, err := taskService.GetTasks()
	if err != nil {
		err.Handle(r, w)
		return
	}
	var result []*ExecedTask
	for _,v:=range tasks{

		taskStatus,ok:=v.Status[nodeUID]
		if ok{
			status:=strings.ToLower(taskStatus.Status)
			if status=="complete" ||status=="start"{
				var execedTask =&ExecedTask{}
				execedTask.ID=v.ID
				execedTask.Status=taskStatus.Status
				execedTask.Depends=[]string{}
				dealDepend(execedTask,v)
				dealNext(execedTask,tasks)
				result=append(result, execedTask)
				continue
			}

		}else {
			_,scheduled:=v.Scheduler.Status[nodeUID]
			if scheduled {
				var execedTask =&ExecedTask{}
				execedTask.ID=v.ID
				execedTask.Depends=[]string{}
				dealDepend(execedTask,v)
				dealNext(execedTask,tasks)
				allDepDone:=true

				for _,dep:=range execedTask.Depends {
					task,_:=taskService.GetTask(dep)

					_,depOK:=task.Status[nodeUID]
					if !depOK {
						allDepDone=false
						break
					}
				}
				//所有依赖都完成了，没有自己的status说明自己未完成，已经被调度说明 正在执行
				if allDepDone {
					execedTask.Status="start"
					result=append(result, execedTask)
				}

			}
		}
	}


	httputil.ReturnSuccess(r, w, result)
}
func dealNext(task *ExecedTask, tasks []*model.Task) {
	for _,v:=range tasks {
		if v.Temp.Depends != nil {
			for _,dep:=range v.Temp.Depends{
				if dep.DependTaskID == task.ID {
					task.Next=append(task.Next,v.ID)
				}
			}
		}
	}
}



func dealDepend(result *ExecedTask,task *model.Task) {
	if task.Temp.Depends != nil {
		for _,v:=range task.Temp.Depends{
			result.Depends=append(result.Depends,v.DependTaskID)
		}
	}
}
type ExecedTask struct {
	ID string
	Status string
	Depends []string
	Next []string
}


//DeleteRainbondNode 节点删除
func DeleteRainbondNode(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	err := nodeService.DeleteNode(nodeID)
	if err != nil {
		err.Handle(r, w)
		return
	}
	httputil.ReturnSuccess(r, w, nil)
}

//Cordon 不可调度
func Cordon(w http.ResponseWriter, r *http.Request) {
	nodeUID := strings.TrimSpace(chi.URLParam(r, "node_id"))
	if err := nodeService.CordonNode(nodeUID, true); err != nil {
		err.Handle(r, w)
		return
	}
	httputil.ReturnSuccess(r, w, nil)
}

//UnCordon 可调度
func UnCordon(w http.ResponseWriter, r *http.Request) {
	nodeUID := strings.TrimSpace(chi.URLParam(r, "node_id"))
	if err := nodeService.CordonNode(nodeUID, false); err != nil {
		err.Handle(r, w)
		return
	}
	httputil.ReturnSuccess(r, w, nil)
}

//PutLabel 更新节点标签
func PutLabel(w http.ResponseWriter, r *http.Request) {
	nodeUID := strings.TrimSpace(chi.URLParam(r, "node_id"))
	var label = make(map[string]string)
	in,error:=ioutil.ReadAll(r.Body)
	if error != nil {
		logrus.Errorf("error read from request ,details %s",error.Error())
		return
	}
	error=json.Unmarshal(in,&label)
	if error!=nil {
		logrus.Errorf("error unmarshal labels  ,details %s",error.Error())
		return
	}
	err:= nodeService.PutNodeLabel(nodeUID, label)
	if err != nil {
		err.Handle(r, w)
		return
	}
	httputil.ReturnSuccess(r, w, nil)
}

//DownNode 节点下线，计算节点操作
func DownNode(w http.ResponseWriter, r *http.Request) {
	nodeUID := strings.TrimSpace(chi.URLParam(r, "node_id"))
	node, err := nodeService.DownNode(nodeUID)
	if err != nil {
		err.Handle(r, w)
		return
	}
	httputil.ReturnSuccess(r, w, node)
}

//UpNode 节点上线，计算节点操作
func UpNode(w http.ResponseWriter, r *http.Request) {
	nodeUID := strings.TrimSpace(chi.URLParam(r, "node_id"))
	node, err := nodeService.UpNode(nodeUID)
	if err != nil {
		err.Handle(r, w)
		return
	}
	httputil.ReturnSuccess(r, w, node)
}


//UpNode 节点实例，计算节点操作
func Instances(w http.ResponseWriter, r *http.Request) {
	nodeUID := strings.TrimSpace(chi.URLParam(r, "node_id"))
	node, err := nodeService.GetNode(nodeUID)
	if err != nil {
		err.Handle(r, w)
		return
	}
	ps, error := k8s.GetPodsByNodeName(node.HostName)
	if error != nil {
		httputil.ReturnError(r,w,404,error.Error())
		return
	}

	pods := []*model.Pods{}
	var cpuR int64
	var cpuL int64
	var memR int64
	var memL int64
	capCPU:=node.NodeStatus.Capacity.Cpu().Value()
	capMEM:=node.NodeStatus.Capacity.Memory().Value()
	for _, v := range ps {
		logrus.Infof("pos 's node name is %s and node is %s",v.Spec.NodeName,node.HostName)
		if v.Spec.NodeName != node.InternalIP {
			continue
		}
		pod := &model.Pods{}
		pod.Namespace = v.Namespace
		serviceId := v.Labels["name"]
		if serviceId == "" {
			continue
		}
		pod.Name = v.Name
		pod.Id = serviceId

		lc := v.Spec.Containers[0].Resources.Limits.Cpu().MilliValue()
		cpuL += lc
		lm := v.Spec.Containers[0].Resources.Limits.Memory().Value()

		memL += lm

		rc := v.Spec.Containers[0].Resources.Requests.Cpu().MilliValue()
		cpuR += rc
		rm := v.Spec.Containers[0].Resources.Requests.Memory().Value()

		memR += rm

		logrus.Infof("namespace %s,podid %s :limit cpu %v,requests cpu %v,limit mem %v,request mem %v,cap cpu is %v,cap mem is %v", pod.Namespace, pod.Name, lc, rc, lm, rm,capCPU,capMEM)

		pod.CPURequests = strconv.FormatFloat(float64(rc)/float64(1000), 'f', 2, 64)

		pod.CPURequestsR = strconv.FormatFloat(float64(rc/10)/float64(capCPU), 'f', 1, 64)

		pod.CPULimits = strconv.FormatFloat(float64(lc)/float64(1000), 'f', 2, 64)
		pod.CPULimitsR = strconv.FormatFloat(float64(lc/10)/float64(capCPU), 'f', 1, 64)

		pod.MemoryRequests = strconv.Itoa(int(rm))
		pod.MemoryRequestsR = strconv.FormatFloat(float64(rm*100)/float64(capMEM), 'f', 1, 64)
		pod.TenantName=v.Labels["tenant_name"]
		pod.MemoryLimits = strconv.Itoa(int(lm))
		pod.MemoryLimitsR = strconv.FormatFloat(float64(lm*100)/float64(capMEM), 'f', 1, 64)

		pods = append(pods, pod)
	}
	httputil.ReturnSuccess(r, w, pods)
}

//NodeExporter 节点监控
func NodeExporter(w http.ResponseWriter, r *http.Request) {
	// filters := r.URL.Query()["collect[]"]
	// logrus.Debugln("collect query:", filters)
	filters := []string{"cpu", "diskstats", "filesystem", "ipvs", "loadavg", "meminfo", "netdev", "netstat", "uname", "mountstats", "nfs"}
	nc, err := collector.NewNodeCollector(filters...)
	if err != nil {
		logrus.Warnln("Couldn't create", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf("Couldn't create %s", err)))
		return
	}
	registry := prometheus.NewRegistry()
	err = registry.Register(nc)
	if err != nil {
		logrus.Errorln("Couldn't register collector:", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("Couldn't register collector: %s", err)))
		return
	}
	gatherers := prometheus.Gatherers{
		prometheus.DefaultGatherer,
		registry,
	}
	// Delegate http serving to Prometheus client library, which will call collector.Collect.
	h := promhttp.HandlerFor(gatherers,
		promhttp.HandlerOpts{
			ErrorLog:      logrus.StandardLogger(),
			ErrorHandling: promhttp.ContinueOnError,
		})
	h.ServeHTTP(w, r)
}

//临时存在
func outJSON(w http.ResponseWriter, data interface{}) {
	outJSONWithCode(w, http.StatusOK, data)
}
func outRespSuccess(w http.ResponseWriter, bean interface{}, data []interface{}) {
	outRespDetails(w, 200, "success", "成功", bean, data)
	//m:=model.ResponseBody{}
	//m.Code=200
	//m.Msg="success"
	//m.MsgCN="成功"
	//m.Body.List=data
}
func outRespDetails(w http.ResponseWriter, code int, msg, msgcn string, bean interface{}, data []interface{}) {
	w.Header().Set("Content-Type", "application/json")
	m := model.ResponseBody{}
	m.Code = code
	m.Msg = msg
	m.MsgCN = msgcn
	m.Body.List = data
	m.Body.Bean = bean

	s := ""
	b, err := json.Marshal(m)

	if err != nil {
		s = `{"error":"json.Marshal error"}`
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		s = string(b)
		w.WriteHeader(code)
	}
	fmt.Fprint(w, s)
}
func outJSONWithCode(w http.ResponseWriter, httpCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	s := ""
	b, err := json.Marshal(data)
	fmt.Println(string(b))
	if err != nil {
		s = `{"error":"json.Marshal error"}`
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		s = string(b)
		w.WriteHeader(httpCode)
	}
	fmt.Fprint(w, s)
}
