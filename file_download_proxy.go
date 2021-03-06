package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//3GB limit
const LIMIT_SIZE = 3 * 1024 * 1024 * 1024

// relative dir
const DOWNLOAD_DIRNAME = "download"

//aria2c 配置
const ARIA2_ADD_URL_METHOD = "aria2.addUri"
const ARIA2_TELL_STATUS_METHOD = "aria2.tellStatus"
const ARIA2_REMOVE_DOWNLOAD_RESULT = "aria2.removeDownloadResult"

var safe_filename_regexp = regexp.MustCompile(`[\w\d.]+`)
var header_filename_regexp = regexp.MustCompile(`[Cc]ontent-[Dd]isposition: ?attachment; ?filename=(.*)`)
var content_length_regexp = regexp.MustCompile(`[Cc]ontent-[Ll]ength: ?(\d+)`)
//refused to download testfile regexp
var testfile_filename_regexp = regexp.MustCompile(`(test)?\d+[MmGg][Bb]?-.*`)

var files_info = map[string]*FileInfo{}
var is_aria2c_running bool

var bind_addr string
var index_data bytes.Buffer

type FileInfo struct {
	FileName       string
	SourceUrl      string
	Size           int64
	ContentLength  int64
	StartTimeStamp int64
	Duration       int64
	Speed          int64
	IsDownloaded   bool
	IsError        bool
}

//aria2c rpc
type Aria2JsonRPCReq struct {
	Method  string `json:"method"`
	Jsonrpc string `json:"jsonrpc"`
	Id      string `json:"id"`
	Params  []interface{} `json:"params"`
}
type Aria2JsonRPCError struct {
	Code    int64 `json:"code"`
	Message string `json:"message"`
}
type Aria2JsonRPCResp struct {
	Id      string `json:"id"`
	Jsonrpc string `json:"jsonrpc"`
	Result  interface{} `json:"result"`
	Error   Aria2JsonRPCError
}

func init() {
	//parse addr:port args
	if len(os.Args) > 1 {
		bind_addr = os.Args[1]
	} else {
		panic("\nUsage: go run file_download_proxy.go addr:port\nExample:go run file_download_proxy.go 127.0.0.1:8000")
	}
	//cache index template
	index_template, _ := template.ParseFiles("index.html")
	type Context struct {
		Bind_addr string
	}
	context := Context{Bind_addr: bind_addr}
	index_template.Execute(&index_data, context)
}

//list files handler
func files_info_handler(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET":
		var response []byte
		list_files(DOWNLOAD_DIRNAME)
		response, _ = json.Marshal(files_info)
		w.Header().Set("Content-Type", "json")
		w.Write(response)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

//file_operation_handler handle file download(get) / create download task(post) / delete_file(delete)
func file_operation_handler(w http.ResponseWriter, req *http.Request) {
	filename := req.URL.Query().Get("filename")
	switch req.Method {
	case "GET":
		if filename == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		log.Printf("Download %v", filename)
		http.Redirect(w, req, "/download/"+filename, http.StatusTemporaryRedirect)
	case "POST":
		download_url := req.PostFormValue("url")
		if download_url == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var response []byte
		// check total size
		files_size := list_files(DOWNLOAD_DIRNAME)
		if files_size > LIMIT_SIZE {
			w.WriteHeader(http.StatusServiceUnavailable)
			response, _ = json.Marshal(map[string]interface{}{"Message": "There are too many files in server, please delete some files", "FilesSize": get_human_size_string(files_size)})
		} else {
			new_file_info := new(FileInfo)
			new_file_info.SourceUrl = download_url
			new_file_info.FileName = get_safe_filename(download_url)
			files_info[new_file_info.FileName] = new_file_info
			go fetch_file(new_file_info)
			w.WriteHeader(http.StatusCreated)
			response, _ = json.Marshal(map[string]string{"Message": "CREATE OK"})
		}
		w.Header().Set("Content-Type", "json")
		w.Write(response)
	case "DELETE":
		log.Printf("Delete %v", filename)
		var response []byte
		if filename == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err := delete_file(filename)
		if err != nil {
			log.Printf("Delete Error when delete %v:%v", filename, err)
			w.WriteHeader(http.StatusNotFound)
			response, _ = json.Marshal(map[string]string{"Message": err.Error()})
		} else {
			response, _ = json.Marshal(map[string]string{"Message": "DELETE OK"})
		}

		w.Header().Set("Content-Type", "json")
		w.Write(response)

	default:
		w.WriteHeader(http.StatusBadRequest)

	}
}

func list_files(dirname string) int64 {
	var file_size int64
	files, _ := ioutil.ReadDir(dirname)
	for _, file := range files {
		if file.IsDir() {
			continue
		} else {
			file_info := files_info[file.Name()]
			if file_info == nil {
				//rebuild new local file
				file_size := file.Size()
				filename := file.Name()
				new_file_info := FileInfo{
					FileName:       filename,
					SourceUrl:      "Local",
					Size:           file_size,
					ContentLength:  file_size,
					StartTimeStamp: file.ModTime().Unix(),
					Duration:       0,
					Speed:          0,
					IsDownloaded:   true,
					IsError:        false}
				files_info[filename] = &new_file_info

			}
			if file_info.Size != file.Size() {
				file_info.Size = file.Size()
			}
			if (! file_info.IsDownloaded) && (!file_info.IsError) {
				duration := time.Now().Unix() - file_info.StartTimeStamp
				if duration > 0 {
					file_info.Speed = file_info.Size / duration
				}
			} else {
				file_info.ContentLength = file_info.Size
			}
			file_size += int64(math.Max(float64(file.Size()), float64(file_info.ContentLength)))
		}
	}
	return file_size
}

func delete_file(filename string) error {
	file_info := files_info[filename]
	if file_info != nil {
		if file_info.IsDownloaded {
			if !file_info.IsError {
				err := os.RemoveAll(DOWNLOAD_DIRNAME + "/" + filename)
				if err != nil {
					return err
				}
			}
			delete(files_info, filename)
			return nil
		}
		return errors.New("file is downloading..")

	}
	log.Println(filename, files_info)
	return errors.New("no such file or direcotry..")

}
func get_content_length_and_attachment_filename(url string) (int64, string, error) {
	output, err := exec.Command("curl", "-IL", url).Output()
	if err != nil {
		return 0, "", err
	}
	output_str := string(output)
	//parse content_length
	var content_length int64
	content_lengths := content_length_regexp.FindAllStringSubmatch(output_str, -1)
	if content_lengths != nil {
		content_length, _ = strconv.ParseInt(content_lengths[len(content_lengths)-1][1], 10, 64)
	} else {
		content_length = 0
	}
	// parse attachment_name
	var attachment_name string
	attachment_names := header_filename_regexp.FindAllStringSubmatch(output_str, -1)
	if attachment_names != nil {
		attachment_name = attachment_names[len(attachment_names)-1][1]
	} else {
		attachment_name = ""
	}
	return content_length, attachment_name, nil
}

func handle_fetch_file_error(file_info *FileInfo, err_message string) {
	log.Println(err_message)
	file_info.IsError = true
	file_info.SourceUrl += err_message
}
func fetch_file(file_info *FileInfo) {
	source_url := file_info.SourceUrl
	if testfile_filename_regexp.MatchString(file_info.FileName) {
		err_message := "refused to download testfile:"
		handle_fetch_file_error(file_info, err_message)
		return
	}
	if strings.HasPrefix(source_url, "http") {
		// http
		//一些资源是动态生成的,请求第一次是chuncked stream,Header不带Content-Length,第二次请求就有Content-length
		content_length, attachment_name, err := get_content_length_and_attachment_filename(source_url)
		if content_length != 0 {
			file_info.ContentLength = content_length
		} else {
			content_length, attachment_name, err = get_content_length_and_attachment_filename(source_url)
		}
		if err != nil {
			err_message := fmt.Sprintf("curl error:%v source_url:", err)
			handle_fetch_file_error(file_info, err_message)
			return
		}
		// if header has attach filename update it
		if attachment_name != "" {
			attachment_name = get_safe_filename(attachment_name)
			files_info[attachment_name] = &FileInfo{
				FileName:       attachment_name,
				SourceUrl:      file_info.SourceUrl,
				Size:           file_info.Size,
				ContentLength:  file_info.ContentLength,
				StartTimeStamp: file_info.StartTimeStamp,
				Duration:       file_info.Duration,
				Speed:          file_info.Speed,
				IsDownloaded:   file_info.IsDownloaded,
				IsError:        file_info.IsError,
			}
			delete(files_info, file_info.FileName)
			file_info = files_info[attachment_name]
		}
		log.Printf("Create Download: length:%s source:%s filename:%s \n", get_human_size_string(file_info.ContentLength), source_url, file_info.FileName)
		if content_length > LIMIT_SIZE {
			err_message := "The content length of source_url is too big :"
			handle_fetch_file_error(file_info, err_message)
			return
		}
		file_info.StartTimeStamp = time.Now().Unix()
		cmd := exec.Command("wget", "-O", "download/"+file_info.FileName, source_url)
		if err := cmd.Start(); err != nil {
			err_message := fmt.Sprintf("wget error:%v source_url:", err)
			handle_fetch_file_error(file_info, err_message)
			return
		}
		cmd.Wait()
	} else if strings.HasPrefix(source_url, "magnet:?xt=urn:btih:") {
		//support magnet
		// check aria2c
		if ! is_aria2c_running {
			err_message := "aria2c is not running,cannot download magnet"
			handle_fetch_file_error(file_info, err_message)
			return
		}
		//send json rpc
		aria2_task_id := file_info.FileName
		response, err := rpc_call_aria2c(ARIA2_ADD_URL_METHOD, aria2_task_id, []interface{}{[]string{source_url}})
		if err != nil {
			err_message := fmt.Sprintf("rpc_call error when calling aria2.addUrl %v source_url:", err)
			handle_fetch_file_error(file_info, err_message)
			return
		}
		task_gid := response.Result
		file_info.StartTimeStamp = time.Now().Unix()
		// get task info
		task_status := "active"
		update_file_name := false
		for task_status != "complete" {
			time.Sleep(time.Second * 5)
			response, err = rpc_call_aria2c(ARIA2_TELL_STATUS_METHOD, aria2_task_id, []interface{}{task_gid})
			if err != nil {
				err_message := fmt.Sprintf("rpc_call error when calling aria2.tellStatus %v source_url:", err)
				handle_fetch_file_error(file_info, err_message)
				return
			}
			result := response.Result.(map[string]interface{})
			task_status = result["status"].(string)
			// check error message
			result_error_message := result["errorMessage"]
			if !(result_error_message == nil || result_error_message == "") {
				err_message := fmt.Sprintf("aria2 error:%v source_url:", result_error_message)
				handle_fetch_file_error(file_info, err_message)
				return
			}
			//arai2c 返回的totalLength的很奇怪
			//total_length := result["totalLength"].(string)
			//file_info.ContentLength, _ = strconv.ParseInt(total_length, 10, 64)
			// 磁力链接建立任务时无法指定文件名 获得真实文件名后需要重命名
			if ! update_file_name {
				real_filename := strings.Replace(result["files"].([]interface{})[0].(map[string]interface{})["path"].(string), "[METADATA]", "", 1)
				//检查同名文件以下载同名文件以免覆盖已下载文件
				if files_info[real_filename] != nil {
					err_message := fmt.Sprintf("file %v is exist. source_url:", real_filename)
					handle_fetch_file_error(file_info, err_message)
					return
				}
				files_info[real_filename] = &FileInfo{
					FileName:       real_filename,
					SourceUrl:      file_info.SourceUrl,
					Size:           file_info.Size,
					ContentLength:  file_info.ContentLength,
					StartTimeStamp: file_info.StartTimeStamp,
					Duration:       file_info.Duration,
					Speed:          file_info.Speed,
					IsDownloaded:   file_info.IsDownloaded,
					IsError:        file_info.IsError,
				}
				delete(files_info, file_info.FileName)
				update_file_name = true
				file_info = files_info[real_filename]
			}
			result_json, _ := json.Marshal(response.Result)
			log.Printf("aria2 status:%s\n", result_json)

		}
		//aria2.removeDownloadResult
		response, err = rpc_call_aria2c(ARIA2_REMOVE_DOWNLOAD_RESULT, aria2_task_id, []interface{}{task_gid})
		if err != nil {
			log.Printf("rpc_call error when calling aria2.removeDownloadResult %v\n", err)
		}

	} else {
		// 既不是http 也不是magnet
		err_message := "do not support this protocol,source_url:"
		handle_fetch_file_error(file_info, err_message)
		return
	}
	// finish download update file_info
	file_info.Duration = time.Now().Unix() - file_info.StartTimeStamp
	if file_info.Duration > 0 {
		if file_info.ContentLength > 0 {
			file_info.Speed = file_info.ContentLength / file_info.Duration
		} else {
			sys_file_info, err := os.Stat(DOWNLOAD_DIRNAME + "/" + file_info.FileName)
			if err != nil {
				file_info.Speed = sys_file_info.Size() / file_info.Duration
			}
		}
	}
	file_info.IsDownloaded = true
}

//utils
func get_safe_filename(url string) string {
	_, filename_in_url := path.Split(url)
	filename := strings.Join(safe_filename_regexp.FindAllString(filename_in_url, -1), "")
	if len_of_filename := len(filename); len_of_filename > 50 {
		filename = filename[len_of_filename-50: len_of_filename]
	}
	file_ext := path.Ext(filename)
	return fmt.Sprintf("%s-%v%s", strings.Replace(filename, file_ext, "", -1), time.Now().Unix(), file_ext)

}
func get_human_size_string(byte_size int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "EB"}
	index := 0
	byte_size_float := float64(byte_size)
	for ; byte_size_float > 1024; index += 1 {
		byte_size_float /= 1024
	}
	return fmt.Sprintf("%.2f %s", byte_size_float, units[index])
}
func has_aria2c() bool {
	output, _ := exec.Command("hash", "aria2c").Output()
	if len(output) == 0 {
		return true
	}
	return false
}
func rpc_call_aria2c(method string, id string, params []interface{}) (*Aria2JsonRPCResp, error) {
	var response Aria2JsonRPCResp
	rpc_request, err := json.Marshal(Aria2JsonRPCReq{Method: method, Jsonrpc: "2.0", Id: id, Params: params })
	if err != nil {
		log.Printf("json marshal error %v %s\n", err, rpc_request)
		return &response, err
	}
	rpc_response, err := http.Post("http://127.0.0.1:6900/jsonrpc", "application/json-rpc", bytes.NewReader(rpc_request))
	if err != nil {
		log.Println("jsonrpc call error", err.Error())
		return &response, err
	}
	defer rpc_response.Body.Close()
	rpc_body, err := ioutil.ReadAll(rpc_response.Body)
	if err != nil {
		log.Println("jsonrpc response read error", err.Error())
		return &response, err
	}
	err = json.Unmarshal(rpc_body, &response)
	if err != nil {
		log.Printf("json unmarshal error %v %s\n", err, rpc_body)
		return &response, err
	}
	return &response, err
}
func main() {
	//make dir and init
	os.Mkdir("download", 0777)
	files, _ := ioutil.ReadDir(DOWNLOAD_DIRNAME)
	for _, file := range files {
		if file.IsDir() {
			continue
		} else {
			file_size := file.Size()
			filename := file.Name()
			new_file_info := FileInfo{
				FileName:       filename,
				SourceUrl:      "Local",
				Size:           file_size,
				ContentLength:  file_size,
				StartTimeStamp: file.ModTime().Unix(),
				Duration:       0,
				Speed:          0,
				IsDownloaded:   true,
				IsError:        false}
			files_info[filename] = &new_file_info
		}
	}
	// running aria2c with enable rpc once
	if has_aria2c() && !is_aria2c_running {
		go func() {
			is_aria2c_running = true
			cmd := exec.Command("aria2c", "--dir=download", "--enable-rpc", "--rpc-listen-port=6900", "--rpc-listen-all=false")
			err := cmd.Start()
			if err != nil {
				log.Println("aria2c can not start :", err.Error())
				is_aria2c_running = false
			}
			time.Sleep(0)
			cmd.Wait()
		}()
	} else {
		log.Println("aria2c not install,cannot download magnet")
	}
	//http server
	//http.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("static/"))))
	http.Handle("/download/", http.StripPrefix("/download", http.FileServer(http.Dir("download"))))
	http.HandleFunc("/file_download_proxy/files", files_info_handler)
	http.HandleFunc("/file_download_proxy/file", file_operation_handler)
	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, req *http.Request) {
		http.ServeFile(w, req, "favicon.ico")
	})
	http.HandleFunc("/file_download_proxy/", func(w http.ResponseWriter, req *http.Request) {
		w.Write(index_data.Bytes())
	})
	log.Printf("service start at %v", bind_addr)
	log.Fatal(http.ListenAndServe(bind_addr, nil))
}
