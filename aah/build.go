// Copyright (c) Jeevanandam M. (https://github.com/jeevatkm)
// go-aah/tools source code and usage is governed by a MIT style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"path"
	"path/filepath"
	"strings"

	"aahframework.org/aah"
	"aahframework.org/aah/router"
	"aahframework.org/config"
	"aahframework.org/essentials"
	"aahframework.org/log"
)

//‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾
// Unexported methods
//___________________________________

// buildApp method calls Go ast parser, generates main.go and builds aah
// application binary at Go bin directory
func buildApp(buildCfg *config.Config) (string, error) {
	// app variables
	appBaseDir := aah.AppBaseDir()
	appImportPath := aah.AppImportPath()
	appCodeDir := filepath.Join(appBaseDir, "app")
	appControllersPath := filepath.Join(appCodeDir, "controllers")

	appName := buildCfg.StringDefault("name", aah.AppName())
	log.Infof("Starting build for '%s' [%s]", appName, appImportPath)

	// excludes for Go AST processing
	excludes, _ := buildCfg.StringList("build.ast_excludes")

	// get all configured Controllers with action info
	registeredActions := router.RegisteredActions()

	// Go AST processing for Controllers
	prg, errs := loadProgram(appControllersPath, ess.Excludes(excludes), registeredActions)
	if len(errs) > 0 {
		errMsgs := []string{}
		for _, e := range errs {
			errMsgs = append(errMsgs, e.Error())
		}
		log.Fatal(strings.Join(errMsgs, "\n"))
	}

	// call the process
	prg.Process()

	// Print router configuration missing/error details
	missingActions := []string{}
	for c, m := range prg.RegisteredActions {
		for a, v := range m {
			if v == 1 && !router.IsDefaultAction(a) {
				missingActions = append(missingActions, fmt.Sprintf("%s.%s", c, a))
			}
		}
	}
	if len(missingActions) > 0 {
		log.Error("Following actions are configured in 'routes.conf', however not implemented in Controller:\n\t",
			strings.Join(missingActions, "\n\t"))
	}

	// get all the types info referred aah framework controller
	appControllers := prg.FindTypeByEmbeddedType(fmt.Sprintf("%s.Controller", aahImportPath))
	appImportPaths := prg.CreateImportPaths(appControllers)

	// prepare aah application version and build date
	appVersion := getAppVersion(appBaseDir, buildCfg)
	appBuildDate := getBuildDate()

	// create go build arguments
	buildArgs := []string{"build"}

	flags, _ := buildCfg.StringList("build.flags")
	buildArgs = append(buildArgs, flags...)

	if ldflags := buildCfg.StringDefault("build.ldflags", ""); !ess.IsStrEmpty(ldflags) {
		buildArgs = append(buildArgs, "-ldflags", ldflags)
	}

	if tags := buildCfg.StringDefault("build.tags", ""); !ess.IsStrEmpty(tags) {
		buildArgs = append(buildArgs, "-tags", tags)
	}

	appBinary := createAppBinaryName(buildCfg)
	appBinaryName := filepath.Base(appBinary)
	buildArgs = append(buildArgs, "-o", appBinary)

	// main.go location e.g. path/to/import/app
	buildArgs = append(buildArgs, path.Join(appImportPath, "app"))

	// clean previous main.go and binary file up before we start the build
	appMainGoFile := filepath.Join(appCodeDir, "aah.go")
	log.Infof("Cleaning %s", appMainGoFile)
	log.Infof("Cleaning %s", appBinary)
	ess.DeleteFiles(appMainGoFile, appBinary)

	generateSource(appCodeDir, "aah.go", aahMainTemplate, map[string]interface{}{
		"AahVersion":     aah.Version,
		"AppImportPath":  appImportPath,
		"AppVersion":     appVersion,
		"AppBuildDate":   appBuildDate,
		"AppBinaryName":  appBinaryName,
		"AppControllers": appControllers,
		"AppImportPaths": appImportPaths,
	})

	// getting project dependencies if not exists in $GOPATH
	if err := checkAndGetAppDeps(appImportPath, buildCfg); err != nil {
		log.Fatal(err)
	}

	// execute aah applictaion build
	if _, err := execCmd(gocmd, buildArgs); err != nil {
		log.Fatal(err)
	}

	log.Infof("Build successful for '%s' [%s].", appName, appImportPath)

	return appBinary, nil
}

func generateSource(dir, filename, templateSource string, templateArgs map[string]interface{}) {
	if !ess.IsFileExists(dir) {
		if err := ess.MkDirAll(dir, 0644); err != nil {
			log.Fatal(err)
		}
	}

	file := filepath.Join(dir, filename)
	buf := &bytes.Buffer{}
	renderTmpl(buf, templateSource, templateArgs)

	if err := ioutil.WriteFile(file, buf.Bytes(), permRWXRXRX); err != nil {
		log.Fatalf("aah '%s' file write error: %s", filename, err)
	}
}

// checkAndGetAppDeps method project dependencies is present otherwise
// it tries to get it if any issues it will return error. It internally uses
// go list command.
// 		go list -f '{{ join .Imports "\n" }}' aah-app/import/path/app/...
//
func checkAndGetAppDeps(appImportPath string, cfg *config.Config) error {
	importPath := path.Join(appImportPath, "app", "...")
	args := []string{"list", "-f", "{{.Imports}}", importPath}

	output, err := execCmd(gocmd, args)
	if err != nil {
		log.Errorf("unable to get application dependencies: %s", err)
		return nil
	}

	lines := strings.Split(strings.TrimSpace(output), "\r\n")
	for _, line := range lines {
		line = strings.Replace(strings.Replace(line, "]", "", -1), "[", "", -1)
		line = strings.Replace(strings.Replace(line, "\r", " ", -1), "\n", " ", -1)
		if ess.IsStrEmpty(line) {
			// all dependencies is available
			return nil
		}

		notExistsPkgs := []string{}
		for _, pkg := range strings.Split(line, " ") {
			if !ess.IsImportPathExists(pkg) {
				notExistsPkgs = append(notExistsPkgs, pkg)
			}
		}

		if cfg.BoolDefault("build.go_get", true) && len(notExistsPkgs) > 0 {
			log.Info("Getting application dependencies ...")
			for _, pkg := range notExistsPkgs {
				args := []string{"get", pkg}
				if _, err := execCmd(gocmd, args); err != nil {
					return err
				}
			}
		} else if len(notExistsPkgs) > 0 {
			log.Error("Below application dependencies are not exists, " +
				"enable 'build.go_get=true' in 'aah.project' for auto fetch")
			log.Fatal("\n", strings.Join(notExistsPkgs, "\n"))
		}
	}

	return nil
}

//‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾‾
// Generate Templates
//___________________________________

const aahMainTemplate = `// aah framework v{{.AahVersion}} - https://aahframework.org
// FILE: aah.go
// GENERATED CODE - DO NOT EDIT

package main

import (
	"flag"
	"fmt"
	"reflect"

	"aahframework.org/aah"
	"aahframework.org/config"
	"aahframework.org/essentials"
	"aahframework.org/log"{{ range $k, $v := $.AppImportPaths }}
	{{$v}} "{{$k}}"{{ end }}
)

var (
	AppBinaryName = "{{.AppBinaryName}}"
	AppVersion = "{{.AppVersion}}"
	AppBuildDate = "{{.AppBuildDate}}"
	_ = reflect.Invalid
)

func main() {
	// Defining flags
	version := flag.Bool("version", false, "Display application version and build date.")
	configPath := flag.String("config", "", "Absolute path of external config file.")
	flag.Parse()

	// display application information
	if *version {
		fmt.Printf("%-12s: %s\n", "Binary Name", AppBinaryName)
		fmt.Printf("%-12s: %s\n", "Version", AppVersion)
		fmt.Printf("%-12s: %s\n", "Build Date", AppBuildDate)
		return
	}

	aah.Init("{{.AppImportPath}}")

	// Loading externally supplied config file
	if !ess.IsStrEmpty(*configPath) {
		externalConfig, err := config.LoadFile(*configPath)
		if err != nil {
			log.Fatalf("Unable to load external config: %s", *configPath)
		}

		aah.MergeAppConfig(externalConfig)
	}

	// Adding all the controllers which refers 'aah.Controller' directly
	// or indirectly from app/controllers/** {{ range $i, $c := .AppControllers }}
	aah.AddController((*{{index $.AppImportPaths .ImportPath}}.{{.Name}})(nil),
	  []*aah.MethodInfo{
	    {{ range .Methods }}&aah.MethodInfo{
	      Name: "{{.Name}}",
	      Parameters: []*aah.ParameterInfo{ {{ range .Parameters }}
	        &aah.ParameterInfo{Name: "{{.Name}}", Type: reflect.TypeOf((*{{.Type.Name}})(nil))},{{ end }}
	      },
	    },
	    {{ end }}
	  })
	{{ end }}

  aah.Start()
}
`
