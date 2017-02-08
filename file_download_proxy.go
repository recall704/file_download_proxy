package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"errors"
)
//2GB
const LIMIT_SIZE = 2 * 1024 * 1024 * 1024
const DOWNLOAD_DIRNAME = "download"

var safe_filename_regexp = regexp.MustCompile(`[\w\s.]+`)
var content_length_regexp = regexp.MustCompile(`[Cc]ontent-[Ll]ength: ?(\d+)`)

var files_info = []*FileInfo{}

type FileInfo struct {
	FileName           string
	SourceUrl          string
	Size               int64
	ContentLength      int64
	HumanSize          string
	HumanContentLength string
	StartTimeStamp     int64
	CompleteTimeStamp  int64
	IsDownloaded       bool
}
//index handler
func index_handler(w http.ResponseWriter, req *http.Request) {
	http.ServeFile(w, req, "index.html")
}
//list files handler
func files_info_handler(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET":
		var response []byte
		files_size := list_files(DOWNLOAD_DIRNAME)
		if files_size > LIMIT_SIZE {
			w.WriteHeader(http.StatusServiceUnavailable)
			response, _ = json.Marshal(map[string]interface{}{"Message":"to many files in server, please delete some files", "FilesSize":files_size})
		} else {
			response, _ = json.Marshal(files_info)
		}
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
		http.ServeFile(w, req, "download/" + filename)
	case "POST":
		download_url := req.PostFormValue("url")
		new_file_info := new(FileInfo)
		new_file_info.SourceUrl = download_url
		new_file_info.FileName = get_safe_filename(download_url)
		new_file_info.IsDownloaded = false
		files_info = append(files_info, new_file_info)
		go wget_file(new_file_info)
		//for calculate download speed roughly
		//time.Sleep(1 * time.Second)
		//http.Redirect(w, req, "/file_download_proxy", 301)
		w.WriteHeader(http.StatusCreated)
		w.Header().Set("Content-Type", "json")
		response, _ := json.Marshal(map[string]string{"Message":"CREATE OK"})
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
			w.WriteHeader(http.StatusInternalServerError)
			response, _ = json.Marshal(map[string]string{"Message":err.Error()})
		} else {
			response, _ = json.Marshal(map[string]string{"Message":"DELETE OK"})
		}

		w.Header().Set("Content-Type", "json")
		w.Write(response)

	default:
		w.WriteHeader(http.StatusBadRequest)

	}
}


//todo optimize O(N*N)
func list_files(dirname string) int64 {
	var file_size int64
	files, _ := ioutil.ReadDir(dirname)
	for _, file := range files {
		if file.IsDir() {
			continue
		} else {
			file_size += file.Size()
			for _, file_info := range files_info {
				if file.Name() == file_info.FileName {
					file_info.Size = file.Size()
					file_info.HumanSize = get_human_size_string(file_info.Size)
					break
				}
			}
		}
	}
	return file_size
}

func delete_file(filename string) error {
	index := 0
	for _, file_info := range files_info {
		if filename == file_info.FileName {
			err := os.Remove("download/" + filename)
			if err != nil {
				return err
			}
			files_info = append(files_info[0:index], files_info[index + 1:]...)
			return nil
		}
		index += 1
	}
	return errors.New("no such file or direcotry")

}

func wget_file(file_info *FileInfo) {
	log.Printf("Download: length:%s source:%s filename:%s \n", file_info.HumanContentLength, file_info.SourceUrl, file_info.FileName)
	output, err := exec.Command("curl", "-IL", file_info.SourceUrl).Output()
	if err != nil {
		log.Println("error in curl", file_info.SourceUrl)
	}
	content_length := content_length_regexp.FindAllStringSubmatch(string(output), -1)
	if content_length != nil {
		file_info.ContentLength, _ = strconv.ParseInt(content_length[len(content_length) - 1][1], 10, 64)
	}
	//fmt.Printf("%v", content_length)
	file_info.HumanContentLength = get_human_size_string(file_info.ContentLength)
	file_info.CompleteTimeStamp = time.Now().Unix()
	cmd := exec.Command("wget", "-O", "download/" + file_info.FileName, file_info.SourceUrl)
	if err = cmd.Start(); err != nil {
		log.Println("error in wget", file_info.SourceUrl)
	}
	cmd.Wait()
	file_info.CompleteTimeStamp = time.Now().Unix()
	file_info.IsDownloaded = true
}

//utils
func get_safe_filename(url string) string {
	_, filename_in_url := path.Split(url)
	filename := strings.Join(safe_filename_regexp.FindAllString(filename_in_url, -1), "")
	file_ext := path.Ext(filename)
	return fmt.Sprintf("%s-%v%s", strings.Replace(filename, file_ext, "", -1), time.Now().Unix(), file_ext)

}
func get_human_size_string(byte_size int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	index := 0
	byte_size_float := float64(byte_size)
	for ; byte_size_float > 1024; index += 1 {
		byte_size_float /= 1024
	}
	return fmt.Sprintf("%.2f %s", byte_size_float, units[index])
}

func main() {
	//make dir and init
	os.Mkdir("download", 0777)
	files, _ := ioutil.ReadDir(DOWNLOAD_DIRNAME)
	for _, file := range files {
		if file.IsDir() {
			continue
		} else {
			new_file_info := FileInfo{
				FileName: file.Name(),
				Size: file.Size(),
				ContentLength:file.Size(),
				SourceUrl: "Local",
				StartTimeStamp:file.ModTime().Unix(),
				CompleteTimeStamp:file.ModTime().Unix(),
				IsDownloaded: true}
			files_info = append(files_info, &new_file_info)
		}
	}
	//http server
	http.Handle("/static/", http.StripPrefix("/static", http.FileServer(http.Dir("static/"))))
	http.HandleFunc("/file_download_proxy/files", files_info_handler)
	http.HandleFunc("/file_download_proxy/file", file_operation_handler)
	http.HandleFunc("/file_download_proxy/", index_handler)
	//parse addr:port args
	var bind_addr string
	if len(os.Args) > 1 {
		bind_addr = os.Args[1]
	} else {
		panic("\nUsage: go run file_download_proxy.go addr:port\nExample:go run file_download_proxy.go :80")
	}
	log.Printf("service start at %v", bind_addr)
	log.Fatal(http.ListenAndServe(bind_addr, nil))
}