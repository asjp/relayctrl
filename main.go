// +build windows
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/moutend/go-hook/pkg/keyboard"
	"github.com/moutend/go-hook/pkg/types"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

type Modifiers map[types.VKCode]bool

type Key struct {
	Id          int
	Key         types.VKCode
	RelayNumber int
	Toggle      bool
	Modifiers   Modifiers
}

type Config struct {
	Mappings  []Key
	Api       string
	Password  string
	Timeout   time.Duration
	NumRelays int
}

var (
	configFile      string
	config          Config
	states          map[int]int
	configByKeyCode map[types.VKCode][]Key
	lastErr         error
	trying			bool
	modifiers       Modifiers
)

type RelayState struct {
	Name  string
	Value int `json:",string"`
}

type CurrentState struct {
	Output []RelayState
}

type CurrentStateResp struct {
	CurrentState CurrentState
}

const css = `
body { margin: 0; font-family: Arial, Helvetica, sans-serif }
#body { margin: 20px }
div { margin: 12px 0 }
.header { font-size: 14pt; padding: 10px; background-color: #262; color: white; margin-top: 0 }
.on { background-color: green; width: 20px }
.off { width: 20px; background-color: orange }
td { text-align: center; border: 1px solid #333; padding: 10px }
table { empty-cells: hide }
.board { background-color: #262; padding: 10px; width: 400px }
.board td { font-weight: bold; border: 2px solid orange }
.ok, .error, .trying { padding: 5px }
.boarderror { background-color: #ccc }
.ok { border: 2px solid green }
.trying { border: 2px solid yellow }
.error { border: 2px solid red }
input[type="checkbox"] { margin-left: 8px }
`

func selectTemplate() string {
	var sb strings.Builder
	sb.WriteString(`<select name="relay{{.Id}}">`)
	for i := 1; i <= config.NumRelays; i++ {
		sb.WriteString(fmt.Sprintf(`<option value="%d"{{if eq .RelayNumber %d}} selected="1"{{end}}>%d</option>`, i, i, i))
	}
	sb.WriteString("</select>")
	return sb.String()
}

func selectKeyTemplate() string {
	var sb strings.Builder
	sb.WriteString(`<select name="key{{.Id}}">`)
	for i := 1; i <= 254; i++ {
		sb.WriteString(fmt.Sprintf(`<option value="%d"{{if eq .Key %d}} selected="1"{{end}}>%s</option>`, i, i, types.VKCode(i)))
	}
	sb.WriteString("</select>")
	return sb.String()
}

func modifiersTemplate() string {
	var sb strings.Builder
	sb.WriteString(`<input type="checkbox" value="1" name="lshift{{.Id}}"{{if index .Modifiers 160}} checked="1"{{end}}/><label for="lshift{{.Id}}">L-Shift</label>`)
	sb.WriteString(`<input type="checkbox" value="1" name="rshift{{.Id}}"{{if index .Modifiers 161}} checked="1"{{end}}/><label for="rshift{{.Id}}">R-Shift</label>`)
	sb.WriteString(`<input type="checkbox" value="1" name="lctrl{{.Id}}"{{if index .Modifiers 162}} checked="1"{{end}}/><label for="lctrl{{.Id}}">L-Ctrl</label>`)
	sb.WriteString(`<input type="checkbox" value="1" name="rctrl{{.Id}}"{{if index .Modifiers 163}} checked="1"{{end}}/><label for="rctrl{{.Id}}">R-Ctrl</label>`)
	return sb.String()
}

func getConfig(saved bool) string {
	var sb strings.Builder
	sb.WriteString("<html><head><title>RelayCtrl Config</title>")
	sb.WriteString("<style>" + css + "</style>")
	sb.WriteString(`</head><body><form method="POST" action="/"><div class="header"><b>RelayCtrl</b> ::: © 2019 Alex Palmer :::</div><div id="body"><div>`)
	t, err := template.New("ip").Parse(`Board IP:&nbsp;<input name="api" type="text" value="{{.}}" />`)
	var b bytes.Buffer
	t.Execute(&b, &config.Api)
	sb.Write(b.Bytes())
	boarderror := ""
	if lastErr == nil {
		if trying {
			sb.WriteString(`<div class="trying">Connecting...</div>`)
		} else {
			sb.WriteString(`<div class="ok">Connected</div>`)
		}
	} else {
		boarderror = " boarderror"
		t, _ := template.New("err").Parse(`<div class="error">Error communicating with relay board! {{.}}</div>`)
		b.Reset()
		t.Execute(&b, &lastErr)
		sb.Write(b.Bytes())
	}
	sb.WriteString("</div>")
	sb.WriteString(`<div class="board` + boarderror + `"><table>`)
	seq := []int{12, 11, 10, 9, 1, 2, 3, 4, 13, 14, 15, 16, 8, 7, 6, 5}
	for i := 0; i < config.NumRelays; i++ {
		if i%8 == 0 {
			if i > 0 {
				sb.WriteString("</tr>")
			}
			sb.WriteString("<tr>")
		}
		if i == 4 || i == 12 {
			sb.WriteString("<td></td>")
		}
		if states[seq[i]] == 0 {
			sb.WriteString(fmt.Sprintf("<td class=\"off\">%v</td>", seq[i]))
		} else {
			sb.WriteString(fmt.Sprintf("<td class=\"on\">%v</td>", seq[i]))
		}
	}
	sb.WriteString("</tr></table></div><br /><hr />")

	sb.WriteString(`<div>Config <input type="submit" value="Save Changes" />`)
	if saved {
		sb.WriteString(`<div style="display: inline-block" class="ok">Saved</div>`)
	}
	sb.WriteString(`</div><table><tr><th>Key Mapping</th><th>Modifiers</th><th>Relay #</th><th>Toggle Mode</th><th>Delete</th></tr>`)
	t, err = template.New("foo").Parse(`<tr><td>` + selectKeyTemplate() + `</td><td>` + modifiersTemplate() + `</td><td>` + selectTemplate() +
		`</td><td><input type="checkbox" name="toggle{{.Id}}" {{if .Toggle}}checked="1" {{end}}/></td><td><input type="button" value="X" onclick="deleteConfig({{.Id}})" /></td></tr>`)
	if err != nil {
		return err.Error()
	}
	for _, k := range config.Mappings {
		var b bytes.Buffer
		t.Execute(&b, &k)
		sb.Write(b.Bytes())
	}
	sb.WriteString(`</table></form><form method="POST" action="/">`)
	sb.WriteString(`<input type="hidden" name="add" value="1" /><input type="submit" value="Add" />`)
	sb.WriteString("</div></form>")
	sb.WriteString(`<form id="deleteform" method="POST" action="/"><input type="hidden" name="delete" value="1" /><input type="hidden" name="id" value="0" /></form>`)
	sb.WriteString(`<script>function deleteConfig(id) { var f = document.getElementById("deleteform"); f.elements.id.value = id; f.submit(); }</script>`)
	sb.WriteString("</body></html>")
	return sb.String()
}

func maxKeyId() int {
	i := -1
	for _, k := range config.Mappings {
		if k.Id > i {
			i = k.Id
		}
	}
	return i
}

func readModifiers(v url.Values, id int) Modifiers {
	m := newModifiers()
	m[types.VK_LSHIFT] = v.Get(fmt.Sprintf("lshift%d", id)) == "1"
	m[types.VK_RSHIFT] = v.Get(fmt.Sprintf("rshift%d", id)) == "1"
	m[types.VK_LCONTROL] = v.Get(fmt.Sprintf("lctrl%d", id)) == "1"
	m[types.VK_RCONTROL] = v.Get(fmt.Sprintf("rctrl%d", id)) == "1"
	return m
}

func updateConfig(v url.Values) error {
	if v.Get("add") != "" {
		config.Mappings = append(config.Mappings, Key{Id: maxKeyId() + 1, Modifiers: newModifiers()})
	} else if v.Get("delete") != "" {
		id_to_delete, _ := strconv.Atoi(v.Get("id"))
		for i, k := range config.Mappings {
			if k.Id == id_to_delete {
				config.Mappings = append(config.Mappings[:i], config.Mappings[i+1:]...)
				break
			}
		}
	} else {
		config.Api = v.Get("api")
		for i, k := range config.Mappings {
			rn, _ := strconv.Atoi(v.Get(fmt.Sprintf("relay%d", k.Id)))
			code, _ := strconv.Atoi(v.Get(fmt.Sprintf("key%d", k.Id)))
			modifiers := readModifiers(v, k.Id)
			config.Mappings[i] = Key{
				Id:          k.Id,
				Key:         types.VKCode(code),
				RelayNumber: rn,
				Toggle:      v.Get(fmt.Sprintf("toggle%d", k.Id)) != "",
				Modifiers:	 modifiers,
			}
		}
		updateConfigByKeyCode()
		initStates()
	}
	return saveConfig()
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if r.Method == "GET" {
		io.WriteString(w, getConfig(r.Form.Get("saved") == "1"))
	} else if r.Method == "POST" {
		if err := updateConfig(r.Form); err != nil {
			http.Error(w, err.Error(), 400)
		} else {
			http.Redirect(w, r, "/?saved=1", 302)
		}
	}
}

func configServer() {
	listen := "localhost:8080"
	fmt.Println("starting config server on " + listen)
	http.HandleFunc("/", rootHandler)

	log.Fatal(http.ListenAndServe(listen, nil))
}

func updateConfigByKeyCode() {
	m := make(map[types.VKCode][]Key, 0)
	for _, c := range config.Mappings {
		//fmt.Println(c)
		_, ok := m[c.Key]
		if !ok {
			m[c.Key] = make([]Key, 0)
		}
		m[c.Key] = append(m[c.Key], c)
	}
	configByKeyCode = m
}

func setRelay(key Key, flags uint32) {
	var on int
	for i, m := range modifiers {
		if key.Modifiers[i] != m {
			return
		}
	}
	if key.Toggle {
		if flags & 128 == 0 {
			if states[key.RelayNumber] == 0 {
				on = 1
			}
		} else {
			return
		}
	} else if flags & 128 == 0 {
		on = 1
	}
	if on != states[key.RelayNumber] {
		//fmt.Printf("%+v %d\n", key, on)
		req, _ := http.NewRequest("GET",
			fmt.Sprintf("http://%v/current_state.json?pw=%v&Relay%d=%d", config.Api, config.Password, key.RelayNumber, on),
			nil)
		ctx, cancelFunc := context.WithTimeout(context.Background(), config.Timeout)
		req = req.WithContext(ctx)
		resp, err := http.DefaultClient.Do(req)
		cancelFunc()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				states[key.RelayNumber] = 1 - states[key.RelayNumber]
			} else {
				lastErr = fmt.Errorf("Got %v response. Is this a relay board?", resp.Status)
			}
		} else {
			lastErr = err
		}
	}
}

func initStates() {
	trying = true
	req, err := http.NewRequest("GET",
		fmt.Sprintf("http://%v/current_state.json?pw=%v", config.Api, config.Password),
		nil)
	ctx, cancelFunc := context.WithTimeout(context.Background(), config.Timeout)
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	cancelFunc()
	if err == nil {
		if resp.StatusCode == 200 {
			var csr CurrentStateResp
			body, err := ioutil.ReadAll(resp.Body)
			if err == nil {
				err = json.Unmarshal(body, &csr)
			}
			resp.Body.Close()
			fmt.Printf("%+v\n", csr)
			if err == nil {
				for i, s := range csr.CurrentState.Output {
					states[i+1] = s.Value
				}
			}
		} else {
			err = fmt.Errorf("Got %v response. Is this a relay board?", resp.Status)
		}
	}
	trying = false
	lastErr = err
}

func loadConfig() error {
	b, err := ioutil.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("Error reading config %v : %v", configFile, err.Error())
	}
	err = json.Unmarshal(b, &config)
	if err != nil {
		return fmt.Errorf("JSON error reading config %v : %v", configFile, err.Error())
	}
	return nil
}

func saveConfig() error {
	b, err := json.Marshal(&config)
	if err != nil {
		return fmt.Errorf("Error converting config to JSON: %v", err.Error())
	}
	err = ioutil.WriteFile(configFile, b, 0644)
	if err != nil {
		return fmt.Errorf("Error writing config to %v : %v", configFile, err.Error())
	}
	return nil
}

func newModifiers() Modifiers {
	m := make(Modifiers, 4)
	m[types.VK_LSHIFT] = false
	m[types.VK_RSHIFT] = false
	m[types.VK_LCONTROL] = false
	m[types.VK_RCONTROL] = false
	return m
}

func main() {
	modifiers = newModifiers()
	
	// default config
	config.Api = "192.168.1.100"
	config.Password = "admin"
	config.Timeout = 250 * time.Millisecond
	config.Mappings = make([]Key, 0)
	config.NumRelays = 16

	// config file is specified as command line arg
	configFile = "config.json"
	if cf := flag.Arg(0); cf != "" {
		configFile = cf
	}

	if err := loadConfig(); err != nil {
		fmt.Println(err.Error())
	}

	updateConfigByKeyCode()
	//fmt.Println(configByKeyCode)

	go configServer()

	states = make(map[int]int, config.NumRelays+1)
	initStates()
	fmt.Println("Starting keyboard capture")

	var isInterrupted bool

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)
	keyboardChan := make(chan types.KeyboardEvent, 100)

	keyboard.Install(nil, keyboardChan)
	defer keyboard.Uninstall()

	var (
		lastkey   types.VKCode
		lastflags uint32
	)
	for {
		if isInterrupted {
			break
		}
		select {
		case <-signalChan:
			isInterrupted = true
		case k := <-keyboardChan:
			//fmt.Printf("%+v\n", k)
			if k.VKCode != lastkey || k.Flags != lastflags {
				if _, ok := modifiers[k.VKCode]; ok {
				    modifiers[k.VKCode] = (k.Flags & 128 == 0)
					//fmt.Printf("%+v\n", modifiers)
				}
				if relays, ok := configByKeyCode[k.VKCode]; ok {
					for _, key := range relays {
						go setRelay(key, k.Flags)
					}
				}
			}
			lastkey = k.VKCode
			lastflags = k.Flags
		}
	}
	fmt.Println("done")
}
