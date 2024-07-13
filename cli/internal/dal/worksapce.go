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

package dal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/leansoftX/smartide-cli/internal/apk/i18n"
	"github.com/leansoftX/smartide-cli/internal/biz/config"
	"github.com/leansoftX/smartide-cli/internal/biz/workspace"
	"github.com/leansoftX/smartide-cli/pkg/common"
	_ "github.com/logoove/sqlite"
	"gopkg.in/yaml.v2"
	//_ "github.com/mattn/go-sqlite3"
)

// key, 只能 16、24 或者 32位
const aesDecryptKey string = "smartide@1O24!QAZWSXedcrfv"

// 多语言
var i18nInstance *i18n.I18nSource = i18n.GetInstance()

// workspace orm
type workspaceDo struct {
	w_id                       int
	w_name                     string
	w_workingdir               sql.NullString
	w_docker_compose_file_path sql.NullString
	w_mode                     string
	w_config_file              sql.NullString
	w_git_clone_repo_url       sql.NullString
	w_git_auth_type            string
	w_git_username             sql.NullString
	w_git_password             sql.NullString
	w_branch                   string
	r_id                       sql.NullInt32
	k_id                       sql.NullInt32
	w_is_del                   bool

	w_json sql.NullString

	w_config_content       sql.NullString
	w_link_compose_content sql.NullString
	w_temp_compose_content sql.NullString

	w_created time.Time
}

// 插入 或者 更新 工作区的数据
func InsertOrUpdateWorkspace(workspaceInfo workspace.WorkspaceInfo) (affectId int64, err error) {
	//1. init
	db := getDb()
	defer db.Close()

	//2. 是否数据已经存在
	isExit := false
	if workspaceInfo.ID != "" { //2.1. 用户录入workspaceid的情况
		i, err := strconv.Atoi(workspaceInfo.ID)
		common.CheckError(err)
		affectId = int64(i)
		isExit = true
	}

	//3. insert or update
	jsonBytes, err := json.Marshal(workspaceInfo.Extend) // 扩展字段序列化
	common.CheckError(err)
	if workspaceInfo.Mode != workspace.WorkingMode_K8s && workspaceInfo.ConfigYaml.IsNil() {
		return -1, errors.New("配置文件数据为空！") //TODO
	}
	if workspaceInfo.TempDockerCompose.IsNil() && workspaceInfo.Mode != workspace.WorkingMode_K8s {
		return -1, errors.New("生成docker-compose数据为空！") //TODO
	}

	//4. 配置文件 及 关联配置
	configStr := ""
	linkComposeStr := ""
	tempComposeStr := ""
	//4.1. k8s 时关联文件格式单独指定
	if workspaceInfo.Mode == workspace.WorkingMode_K8s {
		configStr, _ = workspaceInfo.K8sInfo.OriginK8sYaml.ConvertToConfigYaml()
		linkComposeStr, _ = workspaceInfo.K8sInfo.OriginK8sYaml.ConvertToK8sYaml()
		tempComposeStr, _ = workspaceInfo.K8sInfo.TempK8sConfig.ConvertToK8sYaml()
	} else {
		configStr, _ = workspaceInfo.ConfigYaml.ToYaml()
		linkComposeStr, _ = workspaceInfo.ConfigYaml.Workspace.LinkCompose.ToYaml()
		tempComposeStr, _ = workspaceInfo.TempDockerCompose.ToYaml()
	}
	//4.2. 校验
	/* 	if strings.TrimSpace(configStr) == "" {
	   		return -1, errors.New("配置文件数据为空！")
	   	}
	   	if workspaceInfo.Mode == workspace.WorkingMode_K8s && strings.TrimSpace(linkComposeStr) == "" {
	   		return -1, errors.New("链接K8S yaml文件为空！")
	   	}
	   	if strings.TrimSpace(tempComposeStr) == "" {
	   		return -1, errors.New("生成临时文件为空！")
	   	} */

	//5. insert or update  //TODO: 使用事务，确保成功
	//5.1. 更新关联信息
	remoteId := sql.NullInt32{}                          // 可能是个空值
	k8sId := sql.NullInt32{}                             // 可能是个空值
	if workspaceInfo.Mode != workspace.WorkingMode_K8s { // 插入到 remote 表中
		if (workspaceInfo.Remote != workspace.RemoteInfo{}) {
			tmpId, err := InsertOrUpdateRemote(workspaceInfo.Remote)
			common.CheckError(err)
			if tmpId > 0 {
				remoteId = sql.NullInt32{
					Int32: int32(tmpId),
					Valid: true,
				}
			}
		}
	} else { // 插入到 k8s 表中
		tmpId, err := InsertOrUpdateK8sInfo(workspaceInfo.K8sInfo)
		common.CheckError(err)
		if tmpId > 0 {
			k8sId = sql.NullInt32{
				Int32: int32(tmpId),
				Valid: true,
			}
		}
	}
	//5.2. 数据库表操作
	if !isExit { //5.2.1. insert

		// sql
		stmt, err := db.Prepare(`INSERT INTO workspace(w_name, w_workingdir, w_docker_compose_file_path, w_config_file, r_id,k_id,
												w_mode, w_git_clone_repo_url, w_git_auth_type, w_git_username, w_git_password, w_branch,
												w_json, w_config_content, w_link_compose_content, w_temp_compose_content)  
						VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return -1, err
		}

		res, err := stmt.Exec(workspaceInfo.Name, workspaceInfo.WorkingDirectoryPath, workspaceInfo.TempYamlFileAbsolutePath, workspaceInfo.ConfigFileRelativePath, remoteId, k8sId,
			workspaceInfo.Mode, workspaceInfo.GitCloneRepoUrl, workspaceInfo.GitUserName, workspaceInfo.GitPassword, workspaceInfo.GitRepoAuthType, workspaceInfo.GitBranch,
			string(jsonBytes), configStr, linkComposeStr, tempComposeStr)
		if err != nil {
			return -1, err
		}
		return res.LastInsertId()
	} else { //5.2.2. update
		// exec
		stmt, err := db.Prepare(`update workspace 
								set w_name=?, w_workingdir=?, w_docker_compose_file_path=?, w_config_file=?,
									w_mode=?, w_git_clone_repo_url=?, w_git_auth_type=?, w_git_username=?, w_git_password=?, w_branch=?,
									w_json=?, w_config_content=?, w_link_compose_content=?, w_temp_compose_content=?
								where w_id=?`)
		if err != nil {
			return -1, err
		}
		_, err = stmt.Exec(workspaceInfo.Name, workspaceInfo.WorkingDirectoryPath, workspaceInfo.TempYamlFileAbsolutePath, workspaceInfo.ConfigFileRelativePath,
			workspaceInfo.Mode, workspaceInfo.GitCloneRepoUrl, workspaceInfo.GitUserName, workspaceInfo.GitPassword, workspaceInfo.GitRepoAuthType, workspaceInfo.GitBranch,
			string(jsonBytes), configStr, linkComposeStr, tempComposeStr,
			affectId)
		if err != nil {
			return -1, err
		}
	}

	return affectId, err
}

// 获取工作区列表
func GetWorkspaceList() (workspaces []workspace.WorkspaceInfo, err error) {
	db := getDb()
	defer db.Close()

	rows, err := db.Query(`select w_id, w_name, w_workingdir, w_docker_compose_file_path, w_mode, w_config_file,
									w_git_clone_repo_url, w_git_auth_type, w_git_username, w_git_password, w_branch, r_id, k_id, w_is_del, 
									w_json, w_config_content, w_link_compose_content, w_temp_compose_content, 
									w_created 
							from workspace 
							where w_is_del = 0
							order by w_created desc`)
	for rows.Next() {
		do := workspaceDo{}
		switch errSql := rows.Scan(&do.w_id, &do.w_name, &do.w_workingdir, &do.w_docker_compose_file_path, &do.w_mode, &do.w_config_file,
			&do.w_git_clone_repo_url, &do.w_git_auth_type, &do.w_git_username, &do.w_git_password, &do.w_branch,
			&do.r_id, &do.k_id, &do.w_is_del,
			&do.w_json, &do.w_config_content, &do.w_link_compose_content, &do.w_temp_compose_content,
			&do.w_created); errSql {
		/* case sql.ErrNoRows:
		common.SmartIDELog.Warning() //TODO */
		case nil:

			workspaceInfo := workspace.WorkspaceInfo{}
			err = workspaceDataMap(&workspaceInfo, do)

			workspaces = append(workspaces, workspaceInfo)

		default:
			err = errSql
		}
	}

	return workspaces, err
}

// 获取单个工作区的信息
func GetSingleWorkspaceByParams(workingMode workspace.WorkingModeEnum,
	workingDir string,
	gitCloneUrl string, branch string, confingFilePath string,
	remoteId int, remoteHost string, remoteUserName string) (workspaceInfo workspace.WorkspaceInfo, err error) {
	db := getDb()
	defer db.Close()

	// 查询参数
	params := workspaceDo{}
	params.w_mode = string(workingMode)

	if remoteId <= 0 {
		remoteInfo, err := getRemote(remoteId, remoteHost, remoteUserName)
		common.CheckError(err)
		if remoteInfo != nil && remoteInfo.ID > 0 {
			params.r_id = sql.NullInt32{Int32: int32(remoteInfo.ID), Valid: true}
		}
	} else {
		params.r_id = sql.NullInt32{Int32: int32(remoteId), Valid: true}
	}

	// sql
	var row *sql.Row
	whereStr := " and w_mode=? "
	args := []interface{}{workingMode}
	if workingMode == workspace.WorkingMode_Remote {
		whereStr += " and r_id = ?"
		args = append(args, params.r_id)
	}
	if workingDir != "" { // 远程主机模式时，工作目录是不确定的
		params.w_workingdir = sql.NullString{String: workingDir, Valid: true}
		whereStr += " and w_workingdir = ?"
		args = append(args, params.w_workingdir)
	}
	if gitCloneUrl != "" {
		params.w_git_clone_repo_url = sql.NullString{String: gitCloneUrl, Valid: true}
		whereStr += " and w_git_clone_repo_url = ?"
		args = append(args, params.w_git_clone_repo_url)
	}
	if branch != "" {
		params.w_branch = branch
		whereStr += " and w_branch = ?"
		args = append(args, params.w_branch)
	}
	if confingFilePath != "" {
		params.w_config_file = sql.NullString{String: confingFilePath, Valid: true}
		whereStr += " and w_config_file = ?"
		args = append(args, params.w_config_file)
	}
	row = db.QueryRow(`select w_id, w_name, w_workingdir, w_docker_compose_file_path, w_mode, w_config_file,
								w_git_clone_repo_url, w_git_auth_type, w_git_username, w_git_password, w_branch, 
								r_id, k_id, w_is_del, 
								w_json, w_config_content, w_link_compose_content, w_temp_compose_content, 
								w_created 
							from workspace 
							where w_is_del = 0 `+whereStr, args...)

	// 赋值
	do := workspaceDo{}
	switch err := row.Scan(&do.w_id, &do.w_name, &do.w_workingdir, &do.w_docker_compose_file_path, &do.w_mode, &do.w_config_file,
		&do.w_git_clone_repo_url, &do.w_git_auth_type, &do.w_git_username, &do.w_git_password, &do.w_branch,
		&do.r_id, &do.k_id, &do.w_is_del,
		&do.w_json, &do.w_config_content, &do.w_link_compose_content, &do.w_temp_compose_content,
		&do.w_created); err {
	case sql.ErrNoRows:
		msg := fmt.Sprintf("（%v，%v，%v）", workingDir, workingMode, gitCloneUrl)
		common.SmartIDELog.WarningF(i18nInstance.Common.Warn_dal_record_not_exit_condition, msg)
	case nil:
		err = workspaceDataMap(&workspaceInfo, do)
		return workspaceInfo, err
	default:
		panic(err)
	}

	// return
	return workspaceInfo, err
}

// 把数据库对象进行转换
func workspaceDataMap(workspaceInfo *workspace.WorkspaceInfo, do workspaceDo) error {

	//1. 基本信息
	workspaceInfo.ID = strconv.Itoa(do.w_id)
	workspaceInfo.Name = do.w_name
	workspaceInfo.WorkingDirectoryPath = do.w_workingdir.String
	workspaceInfo.ConfigFileRelativePath = do.w_config_file.String
	workspaceInfo.TempYamlFileAbsolutePath = do.w_docker_compose_file_path.String

	//2. 类型
	if do.w_mode == string(workspace.WorkingMode_Local) {
		workspaceInfo.Mode = workspace.WorkingMode_Local
	} else if do.w_mode == string(workspace.WorkingMode_Remote) {
		workspaceInfo.Mode = workspace.WorkingMode_Remote
	} else if do.w_mode == string(workspace.WorkingMode_K8s) {
		workspaceInfo.Mode = workspace.WorkingMode_K8s
	} else {
		panic("w_mode != string(WorkingMode_Local)")
	}
	workspaceInfo.CacheEnv = workspace.CacheEnvEnum_Local
	workspaceInfo.CliRunningEnv = workspace.CliRunningEnvEnum_Client

	//3. 初始化配置文件
	configYaml, _, _ := config.NewComposeConfigFromContent(do.w_config_content.String, do.w_link_compose_content.String)
	workspaceInfo.ConfigYaml = *configYaml
	if do.w_mode == string(workspace.WorkingMode_Remote) {

		//workspaceInfo.ConfigYaml = *configYaml

	} else if do.w_mode == string(workspace.WorkingMode_K8s) {
		originK8sYaml, err := config.NewK8sConfigFromContent(do.w_config_content.String, do.w_link_compose_content.String)
		if err != nil {
			return err
		}
		workspaceInfo.K8sInfo.OriginK8sYaml = *originK8sYaml

	} else {
		//workspaceInfo.ConfigYaml = *config.NewConfig(do.w_workingdir.String, do.w_config_file.String, do.w_config_content.String)

	}

	//4. 关联
	if !(do.w_mode == string(workspace.WorkingMode_K8s)) {
		// 连接的docker-compose文件
		if do.w_link_compose_content.String != "" {
			err := yaml.Unmarshal([]byte(do.w_link_compose_content.String), &workspaceInfo.ConfigYaml.Workspace.LinkCompose)
			if err != nil {
				return err
			}
		}

		// 生成的docker-compose文件
		if do.w_temp_compose_content.String != "" {
			err := yaml.Unmarshal([]byte(do.w_temp_compose_content.String), &workspaceInfo.TempDockerCompose)
			if err != nil {
				return err
			}
		}
	} else {
		tempK8sYaml, _ := config.NewK8sConfigFromContent(do.w_config_content.String, do.w_temp_compose_content.String)
		workspaceInfo.K8sInfo.TempK8sConfig = *tempK8sYaml
	}

	//5. 扩展属性
	if do.w_json.String != "" {
		err := json.Unmarshal([]byte(do.w_json.String), &workspaceInfo.Extend)
		if err != nil {
			return err
		}
	}

	// git 验证方式
	switch do.w_git_auth_type {
	case string(workspace.GitRepoAuthType_SSH):
		workspaceInfo.GitRepoAuthType = workspace.GitRepoAuthType_SSH
	case string(workspace.GitRepoAuthType_Basic):
		workspaceInfo.GitRepoAuthType = workspace.GitRepoAuthType_Basic
	default:
		workspaceInfo.GitRepoAuthType = workspace.GitRepoAuthType_Public
	}

	// git 相关
	workspaceInfo.GitCloneRepoUrl = do.w_git_clone_repo_url.String
	workspaceInfo.GitBranch = do.w_branch
	workspaceInfo.GitUserName = do.w_git_username.String
	workspaceInfo.GitPassword = do.w_git_password.String

	// 远程主机信息
	if workspaceInfo.Mode == workspace.WorkingMode_Remote {
		rid := int(do.r_id.Int32)
		if rid > 0 {
			remote, err := GetRemoteById(rid)
			if err != nil {
				return err
			}
			if remote == nil {
				return fmt.Errorf("本地查不到不到 id（%v）的远程主机数据", rid)
			}
			workspaceInfo.Remote = *remote
		}
	} else if workspaceInfo.Mode == workspace.WorkingMode_K8s {
		kid := int(do.k_id.Int32)
		if int(kid) > 0 {
			temp, _ := GetK8sInfoById(kid)
			if temp != nil {
				workspaceInfo.K8sInfo.ID = temp.ID
				workspaceInfo.K8sInfo.Context = temp.Context
				workspaceInfo.K8sInfo.Namespace = temp.Namespace
				workspaceInfo.K8sInfo.KubeConfigFilePath = temp.KubeConfigFilePath
			}
		}
	}

	// 其他
	workspaceInfo.CreatedTime = do.w_created

	return nil
}

// 获取工作空间的详情数据
func GetSingleWorkspace(workspaceid int) (workspaceInfo workspace.WorkspaceInfo, err error) {

	db := getDb()
	defer db.Close()

	do := workspaceDo{}
	row := db.QueryRow(`select w_id, w_name, w_workingdir, w_docker_compose_file_path, w_mode, w_config_file, 
								w_git_clone_repo_url, w_git_auth_type, w_branch, r_id, k_id,
								w_json, w_config_content, w_link_compose_content, w_temp_compose_content, 
								w_created 
					    from workspace 
						where w_id=? and w_is_del = 0`, workspaceid)
	switch err := row.Scan(&do.w_id, &do.w_name, &do.w_workingdir, &do.w_docker_compose_file_path, &do.w_mode, &do.w_config_file,
		&do.w_git_clone_repo_url, &do.w_git_auth_type, &do.w_branch, &do.r_id, &do.k_id,
		&do.w_json, &do.w_config_content, &do.w_link_compose_content, &do.w_temp_compose_content,
		&do.w_created); err {
	case sql.ErrNoRows:
		common.SmartIDELog.WarningF(i18nInstance.Common.Warn_dal_record_not_exit_condition, "workspaceid ("+strconv.Itoa(workspaceid)+")") // 没有查询到数据
	case nil:
		err = workspaceDataMap(&workspaceInfo, do)
	default:
		panic(err)
	}

	return workspaceInfo, err
}

// @title    RemoveWorkspace
// @description   删除工作区
// @auth      jasonchen
// @param     workspaceId        int         "工作区id"
// @return           error
func RemoveWorkspace(workspaceId int) error {
	db := getDb()
	defer db.Close()

	// 数据校验
	var count int
	row := db.QueryRow("select count(1) from workspace where w_id=? and w_is_del = 0", workspaceId)
	switch err := row.Scan(&count); err {
	case sql.ErrNoRows:
		msg := fmt.Sprintf(" workspace (%v) ", workspaceId)
		common.SmartIDELog.WarningF(i18nInstance.Common.Warn_dal_record_not_exit_condition, msg)
	case nil:
		if count <= 0 {
			return errors.New(i18nInstance.Common.Warn_dal_record_not_exit)
		} else if count > 1 {
			return errors.New(i18nInstance.Common.Err_dal_update_count_too_much)
		}
	default:
		panic(err)
	}

	//
	stmt, err := db.Prepare("update workspace set w_is_del=1 where (w_id=?) and w_is_del = 0")
	if err != nil {
		return err
	}
	res, err := stmt.Exec(workspaceId)
	if err != nil {
		return err
	}
	affect, err := res.RowsAffected()
	if err != nil {
		return err
	}

	//
	if affect != 1 {
		if affect <= 0 {
			return errors.New(i18nInstance.Common.Err_dal_update_fail) // 更新失败
		} else if affect > 1 {
			common.SmartIDELog.Warning(i18nInstance.Common.Err_dal_update_count_too_much) // 更新条目 > 1
		}
	}

	return nil
}
