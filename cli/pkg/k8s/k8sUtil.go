/*
SmartIDE - CLI
Copyright (C) 2023 leansoftX.com

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package k8s

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/leansoftX/smartide-cli/pkg/common"
	"github.com/spf13/cobra"

	k8sScheme "k8s.io/client-go/kubernetes/scheme"

	coreV1 "k8s.io/api/core/v1"
)

type KubernetesUtil struct {
	KubectlFilePath string
	Context         string
	Namespace       string
	Commands        string
}

var runIdx int = 0               //background调用计数
const ENV_NAME = "XW_DAEMON_IDX" //环境变量名

func NewK8sUtilWithFile(kubeConfigFilePath string, targetContext string, ns string) (*KubernetesUtil, error) {
	return newK8sUtil(kubeConfigFilePath, "", targetContext, ns)
}

func NewK8sUtilWithContent(kubeConfigContent string, targetContext string, ns string) (*KubernetesUtil, error) {
	return newK8sUtil("", kubeConfigContent, targetContext, ns)
}

func NewK8sUtil(kubeConfigFilePath string, targetContext string, ns string) (*KubernetesUtil, error) {
	return newK8sUtil(kubeConfigFilePath, "", targetContext, ns)
}

func newK8sUtil(kubeConfigFilePath string, kubeConfigContent string, targetContext string, ns string) (*KubernetesUtil, error) {
	if targetContext == "" {
		return nil, errors.New("target k8s context is nil")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	//1. kubectl
	//1.1. kubectl 工具的安装路径
	kubectlFilePath := "~/.ide/kubectl"
	switch runtime.GOOS {
	case "windows":
		kubectlFilePath = common.PathJoin(home, ".ide", "kubectl")
	}
	customFlags := ""

	//1.2. 检查并安装kubectl命令行工具
	common.SmartIDELog.Info("检测kubectl（v1.23.0）是否安装到 \"用户目录/.ide\"")
	err = checkAndInstallKubectl(kubectlFilePath)
	if err != nil {
		return nil, err
	}

	//2. kubeconfig
	absoluteKubeConfigFilePath := ""
	//2.0. valid
	if kubeConfigFilePath != "" && kubeConfigContent != "" {
		return nil, errors.New("配置文件路径 和 文件内容不能同时指定")
	}
	//2.1. 指定kubeconfig
	homeDir, _ := os.UserHomeDir()
	if kubeConfigFilePath != "" {
		if strings.Index(kubeConfigFilePath, "~") == 0 {
			absoluteKubeConfigFilePath = strings.Replace(kubeConfigFilePath, "~", homeDir, -1)
		} else {
			if !filepath.IsAbs(kubeConfigFilePath) { // 非绝对路径的时候，就认为是相对用户目录
				absoluteKubeConfigFilePath = filepath.Join(homeDir, kubeConfigFilePath)
			} else {
				absoluteKubeConfigFilePath = kubeConfigFilePath
			}
		}
		if !common.IsExist(absoluteKubeConfigFilePath) {
			return nil, fmt.Errorf("%v 不存在", absoluteKubeConfigFilePath)
		}
		customFlags += fmt.Sprintf("--kubeconfig %v ", absoluteKubeConfigFilePath)
	}

	//2.2. 更新配置文件的内容
	if kubeConfigContent != "" {
		absoluteKubeConfigFilePath = filepath.Join(homeDir, ".kube/config_smartide")

		err = common.FS.CreateOrOverWrite(absoluteKubeConfigFilePath, kubeConfigContent)
		if err != nil {
			return nil, err
		}

		customFlags += fmt.Sprintf("--kubeconfig %v ", absoluteKubeConfigFilePath)
	}

	//3. 切换到指定的context
	common.SmartIDELog.Info("check default k8s context: " + targetContext)
	currentContext, err := execKubectlCommandCombined(kubectlFilePath, customFlags+" config current-context", "")
	if err != nil {
		common.SmartIDELog.Importance(err.Error())
	}
	if targetContext != strings.TrimSpace(currentContext) { //
		customFlags += fmt.Sprintf("--context %v ", targetContext)
	}

	//3.1. check
	common.SmartIDELog.Info("k8s connection check...")
	checkCommand := fmt.Sprintf(`%v get nodes -o jsonpath="{range .items[*]}{@.metadata.name}:{range @.status.conditions[*]}{@.type}={@.status};{end}{end}" | grep "Ready=True"`, customFlags)
	output, err := execKubectlCommandCombined(kubectlFilePath, checkCommand, "") //customFlags+" get nodes -o json"
	if strings.Contains(output, "Unable to connect to the server") || output == "" {
		return nil, errors.New("无法连接到集群 ")
	}
	if err != nil {
		return nil, err
	}

	//4. namespace 为空时，使用一个随机生成的6位字符作为namespace
	if ns == "" {
		for {
			namespace := common.RandLowStr(6)
			output, err := execKubectlCommandCombined(kubectlFilePath, customFlags+" get namespace "+namespace, "")
			if _, isExitError := err.(*exec.ExitError); !isExitError {
				common.SmartIDELog.ImportanceWithError(err)
				continue
			}
			if strings.Contains(output, "not found") {
				ns = namespace
				break
			}
		}
	}
	customFlags += fmt.Sprintf("--namespace %v ", ns)

	return &KubernetesUtil{
		KubectlFilePath: kubectlFilePath,
		Commands:        customFlags,
		Context:         targetContext,
		Namespace:       ns,
	}, nil
}

type ExecInPodRequest struct {
	ContainerName string

	Command string

	Namespace string

	//Pod apiv1.Pod
}

// 拷贝本地ssh config到pod
func (k *KubernetesUtil) CopyLocalSSHConfigToPod(pod coreV1.Pod, containerName string, runAsUser string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// current user dir
	podCurrentUserHomeDir, err := k.GetPodCurrentUserHomeDirection(pod, containerName, runAsUser)
	if err != nil {
		return err
	}

	// copy
	sshPath := filepath.Join(home, ".ssh/")
	if runtime.GOOS == "windows" {
		sshPath = filepath.Join(home, ".ssh\\")
	}
	err = k.CopyToPod(pod, containerName, sshPath, podCurrentUserHomeDir, runAsUser) //.ssh
	if err != nil {                                                                  //TODO 文件不存在，仅警告
		return err
	}

	// .ssh 目录授权
	if runAsUser != "" {
		podCommand := fmt.Sprintf(`sudo chown -R %v:%v ~/.ssh
sudo chmod -R 700 ~/.ssh`, runAsUser, runAsUser)
		k.ExecuteCommandCombinedInPod(pod, containerName, podCommand, "")
	}

	// chmod
	commad := `sudo echo -e 'Host *\n	StrictHostKeyChecking no' >>  ~/.ssh/config`
	k.ExecuteCommandRealtimeInPod(pod, containerName, commad, runAsUser)

	return nil
}

// 拷贝本地git config到pod
func (k *KubernetesUtil) CopyLocalGitConfigToPod(pod coreV1.Pod, containerName string, runAsUser string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	cmd := exec.Command("git", "config", "--list")
	cmd.Dir = home // 运行目录设置到home
	bytes, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	gitconfigs := "core.autocrlf=true\n"
	gitconfigs = fmt.Sprintf("%s%s", gitconfigs, strings.ReplaceAll(string(bytes), "file:", ""))
	for _, str := range strings.Split(gitconfigs, "\n") {
		str = strings.TrimSpace(str)
		if str == "" {
			continue
		}
		var index = strings.Index(str, "=")
		if index < 0 {
			continue
		}
		var key = str[0:index]
		var value = str[index+1:]
		if strings.Contains(key, "user.name") || strings.Contains(key, "user.email") || strings.Contains(key, "core.autocrlf") {
			gitConfigCmd := fmt.Sprintf(`git config --global --replace-all %v '%v'`, key, value)
			err = k.ExecuteCommandRealtimeInPod(pod, containerName, gitConfigCmd, runAsUser)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// git clone
func (k *KubernetesUtil) GitClone(pod coreV1.Pod,
	containerName string,
	runAsUser string,
	gitCloneUrl string, containerCloneDir string, branch string) error {
	// 设置目录为空时，使用默认的
	if containerCloneDir == "" {
		return errors.New("容器内克隆目录为空！")
	}

	// git repo check
	repoUrl := common.GIT.GetRepositoryUrl(gitCloneUrl)
	if repoUrl != "" {
		command := common.GIT.GetCommand4RepositoryUrl(repoUrl)
		result, err := k.ExecuteCommandCombinedInPod(pod, containerName, command, "")
		if err != nil {
			return err
		}
		httpCode, _ := strconv.Atoi(result)
		customErr := common.GIT.CheckError4RepositoryUrl(gitCloneUrl, httpCode)
		if customErr != nil {
			if customErr.GitRepoAccessStatus == common.GitRepoStatusEnum_NotExists {
				return customErr
			} else {
				common.SmartIDELog.Warning(customErr.Error())
			}
		}
	}

	// 直接 git clone
	cloneCommand := fmt.Sprintf(`	    
		 [[ -d '%v' ]] && echo 'git repo existed！' || ( ([[ -d '%v' ]] && rm -rf %v || echo 'floder not exitsed!') && git clone %v %v)
		 `, //sudo chown -R smartide:smartide %v
		filepath.Join(containerCloneDir, ".git"),
		containerCloneDir, containerCloneDir+"/*",
		gitCloneUrl, containerCloneDir)
	err := k.ExecuteCommandRealtimeInPod(pod, containerName, cloneCommand, runAsUser)
	if err != nil {
		return err
	}

	// 切换到指定的分支
	if branch != "" {
		command := fmt.Sprintf("cd %v && git checkout %v", containerCloneDir, branch)
		err := k.ExecuteCommandRealtimeInPod(pod, containerName, command, runAsUser)
		return err
	}

	return nil
}

// 拷贝文件到pod
func (k *KubernetesUtil) CopyToPod(pod coreV1.Pod, containerName string, srcPath string, destPath string, runAsUser string) error {
	//e.g. kubectl cp /tmp/foo <some-namespace>/<some-pod>:/tmp/bar
	workingDir := ""
	commnad := fmt.Sprintf("cp %v %v/%v:%v", srcPath, k.Namespace, pod.Name, destPath)
	if runtime.GOOS == "windows" {
		baseDir := filepath.Base(srcPath)
		workingDir = strings.Replace(srcPath, baseDir, "", -1)
		commnad = fmt.Sprintf("cp %v %v/%v:%v", baseDir, k.Namespace, pod.Name, destPath)
	}
	err := k.ExecKubectlCommandRealtime(commnad, workingDir, false)
	if err != nil {
		return err
	}

	if runAsUser != "" {
		podCommand := fmt.Sprintf(`sudo chown -R %v:%v %v
	sudo chmod -R 700 %v`, runAsUser, runAsUser, destPath,
			destPath)
		k.ExecuteCommandCombinedInPod(pod, containerName, podCommand, "")
	}

	return nil
}

// 拷贝文件到pod
func (k *KubernetesUtil) GetPodCurrentUserHomeDirection(pod coreV1.Pod, containerName string, runAsUser string) (string, error) {
	tmp, err := k.ExecuteCommandCombinedInPod(pod, containerName, "cd ~ && pwd", runAsUser)
	if err != nil && !common.IsExitError(err) {
		return "", err
	}
	array := strings.Split(tmp, "\n")
	result := ""
	for _, msg := range array {
		if !strings.Contains(msg, "Unable to use a TTY - input is not a terminal or the right kind of file") &&
			msg != "" &&
			strings.Contains(msg, "/") {
			result += msg
		}
	}

	result = strings.TrimSpace(result)

	return result, nil
	/* if filepath.IsAbs(result) {
		 return result, nil
	 } else {
		 return "", fmt.Errorf("%v 非正确的文件路径！", result)
	 } */
}

const (
	Flags_ServerHost      = "serverhost"
	Flags_ServerToken     = "servertoken"
	Flags_ServerOwnerGuid = "serverownerguid"
)

func (k *KubernetesUtil) StartAgent(cmd *cobra.Command, pod coreV1.Pod,
	containerName string, runAsUser string,
	workspaceId uint) {
	fflags := cmd.Flags()
	host, _ := fflags.GetString(Flags_ServerHost)
	token, _ := fflags.GetString(Flags_ServerToken)
	ownerguid, _ := fflags.GetString(Flags_ServerOwnerGuid)

	commad := fmt.Sprintf("sudo chmod +x /smartide-agent && cd /;./smartide-agent --serverhost %s --servertoken %s --serverownerguid %s --workspaceId %v ", host, token, ownerguid, workspaceId)

	go k.ExecuteCommandRealtimeInPod(pod, containerName, commad, runAsUser)

	// 创建supervisor
	commad = fmt.Sprintf(`sudo echo -e '[program:smartide-agent]
directory=/
command=/smartide-agent --serverhost %s --servertoken %s --serverownerguid %s --workspaceId %v
autostart=true
autorestart=true
startretries=10
redirect_stderr=true
stdout_logfile=/smartide-agent.log' >> /etc/supervisor/conf.d/smartide-agent.conf`, host, token, ownerguid, workspaceId)
	err := k.ExecuteCommandRealtimeInPod(pod, containerName, commad, "")
	if err != nil {
		common.SmartIDELog.Debug(err.Error())
	}

	commad = "supervisord"
	err = k.ExecuteCommandRealtimeInPod(pod, containerName, commad, "")
	if err != nil {
		common.SmartIDELog.Debug(err.Error())
	}
	commad = "supervisorctl reload"
	err = k.ExecuteCommandRealtimeInPod(pod, containerName, commad, "")
	if err != nil {
		common.SmartIDELog.Debug(err.Error())
	}
}

type ProxyWriter struct {
	file *os.File
}

func NewProxyWriter(file *os.File) *ProxyWriter {
	return &ProxyWriter{
		file: file,
	}
}

func (w *ProxyWriter) Write(p []byte) (int, error) {
	// ... do something with bytes first
	fmt.Fprintf(w.file, "%s", string(p))
	return len(p), nil
}

func (k *KubernetesUtil) ExecKubectlCommandRealtime(command string, dirctory string, isLoop bool) error {
	var execCommand *exec.Cmd

	kubeCommand := fmt.Sprintf("%v %v %v", k.KubectlFilePath, k.Commands, command)
	if isLoop {
		kubeCommand = fmt.Sprintf("while true; do %v; done", kubeCommand)
	}
	common.SmartIDELog.Debug(kubeCommand)
	switch runtime.GOOS {
	case "windows":
		execCommand = exec.Command("powershell", "/c", kubeCommand)
	default:
		execCommand = exec.Command("bash", "-c", kubeCommand)
	}

	if dirctory != "" {
		execCommand.Dir = dirctory
	}

	//execCommand.Stdout = os.Stdin
	execCommand.Stdout = NewProxyWriter(os.Stdout)
	execCommand.Stderr = NewProxyWriter(os.Stderr)
	return execCommand.Run()
}

func (k *KubernetesUtil) ExecKubectlCommandWithOutputRealtime(command string, dirctory string) (string, error) {
	var execCommand *exec.Cmd

	kubeCommand := fmt.Sprintf("%v %v %v", k.KubectlFilePath, k.Commands, command)
	common.SmartIDELog.Debug(kubeCommand)
	switch runtime.GOOS {
	case "windows":
		execCommand = exec.Command("powershell", "/c", kubeCommand)
	default:
		execCommand = exec.Command("bash", "-c", kubeCommand)
	}

	if dirctory != "" {
		execCommand.Dir = dirctory
	}

	bytes, err := execCommand.CombinedOutput()
	output := string(bytes)
	if strings.Contains(output, "error:") || strings.Contains(output, "fatal:") {
		return "", errors.New(output)
	}
	return string(bytes), err
}

func (k *KubernetesUtil) ExecKubectlCommandRealtimeBackground(command string, dirctory string, isLoop bool, isExit bool) error {

	logFile := "/tmp/daemon.log"
	/*以下是父进程执行的代码*/

	//因为要设置更多的属性, 这里不使用`exec.Command`方法, 直接初始化`exec.Cmd`结构体
	cmd := &exec.Cmd{
		Path: os.Args[0],
		Args: os.Args,      //注意,此处是包含程序名的
		Env:  os.Environ(), //父进程中的所有环境变量
	}

	kubeCommand := fmt.Sprintf("%v %v %v", k.KubectlFilePath, k.Commands, command)
	if isLoop {
		kubeCommand = fmt.Sprintf("while true; do %v; done", kubeCommand)
	}
	common.SmartIDELog.Debug(kubeCommand)

	if dirctory != "" {
		cmd.Dir = dirctory
	}
	//判断子进程还是父进程
	runIdx++
	envIdx, err := strconv.Atoi(os.Getenv(ENV_NAME))
	if err != nil {
		envIdx = 0
	}
	if runIdx <= envIdx { //子进程, 退出
		return nil
	}

	//为子进程设置特殊的环境变量标识
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", ENV_NAME, runIdx))

	//若有日志文件, 则把子进程的输出导入到日志文件
	if logFile != "" {
		stdout, err := os.OpenFile(logFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
		if err != nil {
			log.Println(os.Getpid(), ": 打开日志文件错误:", err)
			return err
		}
		cmd.Stderr = stdout
		cmd.Stdout = stdout
	}
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("powershell", "/c", kubeCommand)
	default:
		cmd = exec.Command("bash", "-c", kubeCommand)
	}
	//异步启动子进程
	err = cmd.Start()
	if err != nil {
		log.Println(os.Getpid(), "启动子进程失败:", err)
		return err
	} else {
		//执行成功
		log.Println(os.Getpid(), ":", "启动子进程成功:", "->", cmd.Process.Pid, "\n ")
	}

	//若启动子进程成功, 父进程是否直接退出
	if isExit {
		os.Exit(0)
	}
	//execCommand.Stdout = os.Stdin
	return nil
}

func (k *KubernetesUtil) ExecKubectlCommand(command string, dirctory string, isLoop bool) error {
	var execCommand *exec.Cmd

	kubeCommand := fmt.Sprintf("%v %v %v", k.KubectlFilePath, k.Commands, command)
	if isLoop {
		kubeCommand = fmt.Sprintf("while true; do %v; done", kubeCommand)
	}
	common.SmartIDELog.Debug(kubeCommand)
	switch runtime.GOOS {
	case "windows":
		execCommand = exec.Command("powershell", "/c", kubeCommand)
	default:
		execCommand = exec.Command("bash", "-c", kubeCommand)
	}

	if dirctory != "" {
		execCommand.Dir = dirctory
	}

	return execCommand.Run()
}

// 一次性执行kubectl命令
func (k *KubernetesUtil) ExecKubectlCommandCombined(command string, dirctory string) (string, error) {
	return execKubectlCommandCombined(k.KubectlFilePath, k.Commands+" "+command, dirctory)
}

func execKubectlCommandCombined(kubectlFilePath string, command string, workingDirctory string) (string, error) {
	var execCommand *exec.Cmd

	kubeCommand := fmt.Sprintf("%v %v", kubectlFilePath, command)

	switch runtime.GOOS {
	case "windows":
		kubeCommand = strings.ReplaceAll(kubeCommand, "grep ", "findstr ")
		execCommand = exec.Command("powershell", "/c", kubeCommand)
	default:
		execCommand = exec.Command("bash", "-c", kubeCommand)
	}

	if workingDirctory != "" {
		execCommand.Dir = workingDirctory
	}

	bytes, err := execCommand.CombinedOutput()
	output := string(bytes)
	common.SmartIDELog.Debug(fmt.Sprintf("%v %v >> %v", workingDirctory, kubeCommand, output))
	if strings.Contains(output, "error:") || strings.Contains(output, "fatal:") {
		return "", errors.New(output)
	}

	return string(bytes), err
}

// 根据selector获取pod
func (k *KubernetesUtil) GetPodInstanceBySelector(selector string) (*coreV1.Pod, error) {
	command := ""
	command = fmt.Sprintf("get pod --selector=%v -o=yaml ", selector)
	yaml, err := k.ExecKubectlCommandCombined(command, "")
	if err != nil {
		return nil, err
	}

	decode := k8sScheme.Codecs.UniversalDeserializer().Decode
	obj, _, _ := decode([]byte(yaml), nil, nil)
	list := obj.(*coreV1.List)

	if list == nil || len(list.Items) == 0 {
		return nil, errors.New("查找不到对应的pod，请检查k8s运行环境是否正常！")
	}

	item := list.Items[0]
	bytes, _ := item.MarshalJSON()
	objPod, _, _ := decode(bytes, nil, nil)
	pod := objPod.(*coreV1.Pod)

	return pod, nil
}

func (k *KubernetesUtil) GetPodInstanceByName(podName string) (*coreV1.Pod, error) {
	if podName == "" {
		return nil, errors.New("pod name is nil")
	}
	command := fmt.Sprintf("get pod %v -o=yaml ", podName)
	yaml, err := k.ExecKubectlCommandCombined(command, "")
	if err != nil {
		return nil, err
	}

	decode := k8sScheme.Codecs.UniversalDeserializer().Decode
	obj, _, _ := decode([]byte(yaml), nil, nil)

	pod := obj.(*coreV1.Pod)

	return pod, nil
}

// 在pod中实时执行shell命令
// example: kubectl -it exec podname -- bash/sh -c
func (k *KubernetesUtil) ExecuteCommandRealtimeInPod(pod coreV1.Pod, containerName string, command string, runAsUser string) error {
	//command = "su smartide -c " + command
	if runAsUser != "" && runAsUser != "root" {
		/* if runtime.GOOS == "windows" {
			command = strings.ReplaceAll(command, "'", "`")
		} else {
			command = strings.ReplaceAll(command, "'", "\"\"")
		} */
		command = strings.ReplaceAll(command, "'", "''")
		command = fmt.Sprintf(`su %v -c '%v'`, runAsUser, command)
	}
	kubeCommand := fmt.Sprintf(` -it exec %v -- /bin/bash -c "%v"`, pod.Name, command)

	err := k.ExecKubectlCommandRealtime(kubeCommand, "", false)
	if err != nil {
		return err
	}

	return nil
}

func (k *KubernetesUtil) ExecuteCommandInPod(pod coreV1.Pod, containerName string, command string, runAsUser string) error {
	//command = "su smartide -c " + command
	kubeCommand := formartCommand(pod, containerName, command, runAsUser)
	err := k.ExecKubectlCommand(kubeCommand, "", false)
	if err != nil {
		return err
	}

	return nil
}

// 在pod中一次性执行shell命令
func (k *KubernetesUtil) ExecuteCommandCombinedInPod(pod coreV1.Pod, containerName string, command string, runAsUser string) (string, error) {
	//command = "su smartide -c " + command
	kubeCommand := formartCommand(pod, containerName, command, runAsUser)
	output, err := k.ExecKubectlCommandCombined(kubeCommand, "")
	return output, err
}

// 在pod中一次性执行shell命令
func (k *KubernetesUtil) ExecuteCommandCombinedBackgroundInPod(pod coreV1.Pod, containerName string, command string, runAsUser string) {
	//command = fmt.Sprintf("su smartide -c '%v'", command)
	kubeCommand := formartCommand(pod, containerName, command, runAsUser)
	k.ExecKubectlCommandCombined(kubeCommand, "")
}

func formartCommand(pod coreV1.Pod, containerName string, command string, runAsUser string) string {
	if runAsUser != "" && runAsUser != "root" {
		if runtime.GOOS == "windows" {
			command = strings.ReplaceAll(command, "'", "`")
		} else {
			command = strings.ReplaceAll(command, "'", "\"\"")
		}
		command = fmt.Sprintf(`su %v -c '%v'`, runAsUser, command)
	}
	containerCommand := ""
	if containerName != "" {
		containerCommand = fmt.Sprintf("--container %v", containerName)
	}
	kubeCommand := fmt.Sprintf(` exec  %v %v -- /bin/bash -c "%v"`, pod.Name, containerCommand, command)
	return kubeCommand
}

// 检查并安装kubectl工具
func checkAndInstallKubectl(kubectlFilePath string) error {

	//1. 在.ide目录下面检查kubectl文件是否存在
	//e.g. Client Version: version.Info{Major:"1", Minor:"23", GitVersion:"v1.23.5", GitCommit:"c285e781331a3785a7f436042c65c5641ce8a9e9", GitTreeState:"clean", BuildDate:"2022-03-16T15:58:47Z", GoVersion:"go1.16.8", Compiler:"gc", Platform:"linux/amd64"}
	// 1.1. 判断是否安装
	isInstallKubectl := true
	output, err := execKubectlCommandCombined(kubectlFilePath, "version --client", "")
	common.SmartIDELog.Debug(output)
	if !strings.Contains(output, "GitVersion:\"v1.23.0\"") {
		isInstallKubectl = false
	} else if err != nil {
		common.SmartIDELog.ImportanceWithError(err)
		isInstallKubectl = false
	}
	if isInstallKubectl { // 如果已经安装，将返回
		return nil
	}

	//2. 如果不存在从smartide dl中下载对应的版本
	common.SmartIDELog.Info("安装kubectl（v1.23.0）工具到 \"用户目录/.ide\" 目录...")
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var execCommand2 *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		kubectlFilePath := strings.Join([]string{home, ".ide", "kubectl.exe"}, string(filepath.Separator)) //common.PathJoin(home, ".ide")
		command := "Invoke-WebRequest -Uri \"https://smartidedl.blob.core.chinacloudapi.cn/kubectl/v1.23.0/bin/windows/amd64/kubectl.exe\" -OutFile " + kubectlFilePath
		common.SmartIDELog.Debug(command)
		execCommand2 = exec.Command("powershell", "/c", command)
	case "darwin":
		command := `curl -OL  "https://smartidedl.blob.core.chinacloudapi.cn/kubectl/v1.23.0/bin/darwin/amd64/kubectl" \
		 && mv -f kubectl ~/.ide/kubectl \
		 && chmod +x ~/.ide/kubectl`
		common.SmartIDELog.Debug(command)
		execCommand2 = exec.Command("bash", "-c", command)
	case "linux":
		command := `curl -OL  "https://smartidedl.blob.core.chinacloudapi.cn/kubectl/v1.23.0/bin/linux/amd64/kubectl" \
		 && mv -f kubectl ~/.ide/kubectl \
		 && chmod +x ~/.ide/kubectl`
		common.SmartIDELog.Debug(command)
		execCommand2 = exec.Command("bash", "-c", command)
	}
	execCommand2.Stdout = os.Stdout
	execCommand2.Stderr = os.Stderr
	err = execCommand2.Run()
	if err != nil {
		return err
	}

	return nil
}

type WorkspaceIngress struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string `yaml:"name"`
		Namespace   string `yaml:"namespace"`
		Annotations struct {
			NginxIngressKubernetesIoAuthType   string `yaml:"nginx.ingress.kubernetes.io/auth-type"`
			NginxIngressKubernetesIoAuthSecret string `yaml:"nginx.ingress.kubernetes.io/auth-secret"`
			NginxIngressKubernetesIoUseRegex   string `yaml:"nginx.ingress.kubernetes.io/use-regex"`
			CertManagerIoClusterIssuer         string `yaml:"cert-manager.io/cluster-issuer"`
		} `yaml:"annotations"`
	} `yaml:"metadata"`
	Spec struct {
		IngressClassName string `yaml:"ingressClassName"`
		TLS              []struct {
			Hosts      []string `yaml:"hosts"`
			SecretName string   `yaml:"secretName"`
		} `yaml:"tls"`
		Rules []struct {
			Host string `yaml:"host"`
			HTTP struct {
				Paths []struct {
					Path     string `yaml:"path"`
					PathType string `yaml:"pathType"`
					Backend  struct {
						Service struct {
							Name string `yaml:"name"`
							Port struct {
								Number int `yaml:"number"`
							} `yaml:"port"`
						} `yaml:"service"`
					} `yaml:"backend"`
				} `yaml:"paths"`
			} `yaml:"http"`
		} `yaml:"rules"`
	} `yaml:"spec"`
}

type WorkspaceIgnoreTLSIngress struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string `yaml:"name"`
		Namespace   string `yaml:"namespace"`
		Annotations struct {
			NginxIngressKubernetesIoAuthType         string `yaml:"nginx.ingress.kubernetes.io/auth-type"`
			NginxIngressKubernetesIoAuthSecret       string `yaml:"nginx.ingress.kubernetes.io/auth-secret"`
			NginxIngressKubernetesIoUseRegex         string `yaml:"nginx.ingress.kubernetes.io/use-regex"`
			NginxIngressKubernetesIoForceSSLRedirect string `yaml:"nginx.ingress.kubernetes.io/force-ssl-redirect"`
			NginxIngressKubernetesIoSSLPassThrough   string `yaml:"nginx.ingress.kubernetes.io/ssl-passthrough"`
		} `yaml:"annotations"`
	} `yaml:"metadata"`
	Spec struct {
		IngressClassName string `yaml:"ingressClassName"`
		Rules            []struct {
			Host string `yaml:"host"`
			HTTP struct {
				Paths []struct {
					Path     string `yaml:"path"`
					PathType string `yaml:"pathType"`
					Backend  struct {
						Service struct {
							Name string `yaml:"name"`
							Port struct {
								Number int `yaml:"number"`
							} `yaml:"port"`
						} `yaml:"service"`
					} `yaml:"backend"`
				} `yaml:"paths"`
			} `yaml:"http"`
		} `yaml:"rules"`
	} `yaml:"spec"`
}

type ConfigMap struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
}
