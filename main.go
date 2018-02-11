package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-ini/ini"
	"github.com/kelseyhightower/envconfig"
	"github.com/pkg/errors"
)

var (
	showUsage          = false
	priorityMirrorList = []string{
		"http://mirror.bebout.net/remi/",
		"http://repo1.sea.innoscale.net/remi/",
		"http://mirrors.mediatemple.net/remi/",
	}

	xmlJPURLRe = regexp.MustCompile(`location="JP".+>(https?://.+)</url>`)
	xmlURLRe   = regexp.MustCompile(`location=".+".+>(https?://.+)</url>`)
	s3URLRe    = regexp.MustCompile(`http.+s3-mirror-ap-northeast-1`)
)

type config struct {
	Arch       string   `envconfig:"ARCH" default:"x86_64"`
	ReleaseVer string   `envconfig:"RELEASEVER" default:"7"`
	ReposDir   string   `envconfig:"REPOSDIR" default:"/etc/yum.repos.d"`
	DestPath   string   `envconfig:"DEST" default:"./dest"`
	Bucket     string   `envconfig:"BUCKET" default:"hoge"`
	Prefix     string   `envconfig:"PREFIX" default:"/mirror-repo/"`
	Ignores    []string `envconfig:"IGNORES" default:"DEFAULT,amzn"`
	BasePath   string   `envconfig:"BASEPATH" default:"https://hoge.cloudfront.net"`
	//RepoFilesDir string `envconfig:"REPOFILESDIR" default:"/etc/yum.repo.d"`

}

func init() {
	rand.Intn(10)
}

func makePath(conf *config, section, url string) string {
	if strings.Contains(url, conf.Arch) {
		res := filepath.Join(conf.Prefix, section, conf.ReleaseVer, conf.Arch)
		return replaceURL(conf, res)
	}
	res := filepath.Join(conf.Prefix, section, conf.ReleaseVer)
	return replaceURL(conf, res)
}

func makeBaseurl(conf *config, section, url string) string {
	conf.BasePath = strings.TrimLeft(conf.BasePath, "/")
	return fmt.Sprintf("%s/%s", conf.BasePath, makePath(conf, section, url))
}

func isIgnore(conf *config, name string) bool {
	for _, ignore := range conf.Ignores {
		if strings.HasPrefix(name, ignore) {
			return true
		}
	}
	return false
}

func loadEnv() (*config, error) {
	var c config
	if err := envconfig.Process("", &c); err != nil {
		return nil, errors.Wrap(err, "LoadEnv parse err")
	}

	return &c, nil
}

func main() {

	conf, err := loadEnv()
	if err != nil {
		log.Fatal(err)
	}
	flag.BoolVar(&showUsage, "u", false, "show usage.")
	flag.Parse()
	if showUsage {
		envconfig.Usage("", conf)
		os.Exit(0)
	}

	filePaths, err := filepath.Glob(filepath.Join(conf.ReposDir, "*.repo"))
	if err != nil {
		log.Fatal(err)
	}
	err = os.MkdirAll(conf.DestPath, 0755)
	if err != nil {
		log.Fatal(err)
	}
	for _, filePath := range filePaths {
		readWriteRepoFile(conf, filePath)
	}

}
func readWriteRepoFile(conf *config, filePath string) {
	filename := filepath.Base(filePath)
	iniData, err := ini.Load(filePath)
	if err != nil {
		log.Fatal(err)
	}
	for _, section := range iniData.Sections() {
		if isIgnore(conf, section.Name()) {
			continue
		}

		//log.Println("section name:", section.Name())
		mirrorlist := section.Key("mirrorlist").String()
		baseurl := section.Key("baseurl").String()
		urls := []string{baseurl}
		if len(mirrorlist) > 0 {
			urls = getMirroList(conf, mirrorlist)
		}
		cmd := make3syncCmdLine(conf, section.Name(), urls)
		if len(urls[0]) > 0 {
			fmt.Println(cmd)
			iniData.Section(section.Name()).Key("baseurl").SetValue(makeBaseurl(conf, section.Name(), urls[0]))
		}
		iniData.Section(section.Name()).DeleteKey("mirrorlist")

	}
	output := filepath.Join(conf.DestPath, filename)
	//log.Printf("out:%s", output)
	err = iniData.SaveTo(output)
	if err != nil {
		log.Fatal(err)
	}
}

func make3syncCmdLine(conf *config, section string, urls []string) string {
	i := rand.Intn(len(urls))
	return fmt.Sprintf("yums3sync --source '%s' --bucket '%s' --prefix '%s'", replaceURL(conf, urls[i]), conf.Bucket, makePath(conf, section, urls[i]))
	//yums3sync --source url --bucket $BUCKET --prefix $PREFIX/
}

func getMirroList(conf *config, url string) []string {
	url = replaceURL(conf, url)
	res, err := http.Get(url)
	if err != nil {
		log.Fatal(err)
	}
	body, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		log.Fatal(err)
	}
	//log.Printf("%s", body)
	return getPriorityURL(getUrls(body))
}

func searchString(lines [][]byte, s string) bool {
	for _, line := range lines {
		if bytes.Contains(line, []byte(s)) {
			return true
		}
	}
	return false

}

func grep(lines [][]byte, r *regexp.Regexp) []string {
	list := make([]string, 0, 300)
	for _, line := range lines {
		res := r.FindAllStringSubmatch(string(line), 1)
		if len(res) == 0 {
			continue
		}
		if len(res[0]) == 0 {
			continue
		}
		list = append(list, res[0][1])
	}
	return list
}

func getPriorityURL(urls []string) []string {
	res := make([]string, 0, len(urls))
	for _, url := range urls {
		if s3URLRe.MatchString(url) {
			res = append(res, url)
		}
	}
	if len(res) > 0 {
		return res
	}
	for _, url := range urls {
		if strings.Contains(url, ".jp/") {
			res = append(res, url)
		}
	}
	if len(res) > 0 {
		return res
	}
	for _, url := range urls {
		for _, p := range priorityMirrorList {
			if strings.HasPrefix(url, p) {
				res = append(res, url)
			}
		}
	}
	if len(res) > 0 {
		return res
	}
	return urls
}

func getUrls(body []byte) []string {
	lines := bytes.Split(body, []byte("\n"))
	if searchString(lines, "<?xml version=") {
		if searchString(lines, `location="JP"`) {
			return grep(lines, xmlJPURLRe)
		}
		return grep(lines, xmlURLRe)
	}
	res := make([]string, 0, len(lines))
	for _, line := range lines {
		l := string(line)
		if strings.HasPrefix(l, "#") {
			continue
		}
		res = append(res, l)
	}
	return res
}

func replaceURL(conf *config, url string) string {
	url = strings.Replace(url, "$basearch", conf.Arch, -1)
	url = strings.Replace(url, "$releasever", conf.ReleaseVer, -1)
	url = strings.Replace(url, "$infra", "", -1)
	url = strings.Replace(url, "/repodata/repomd.xml", "", -1)
	return url
}
