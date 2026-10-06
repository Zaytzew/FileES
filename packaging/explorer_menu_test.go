package packaging_test

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

func TestClassicExplorerMenuUsesOnlyClosedSupervisorGesturesAndUninstalls(t *testing.T) {
	data, err := os.ReadFile("windows/filees.wxs")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Package struct {
			Components []struct {
				ID     string `xml:"Id,attr"`
				Values []struct {
					Root, Key, Name, Value string `xml:",attr"`
				} `xml:"RegistryValue"`
				Removes []struct {
					Root, Key, Action string `xml:",attr"`
				} `xml:"RemoveRegistryKey"`
			} `xml:"Component"`
		} `xml:"Package"`
	}
	if err := xml.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	for _, component := range source.Package.Components {
		if component.ID != "FileESExplorerMenu" {
			continue
		}
		commands := map[string]string{}
		for _, value := range component.Values {
			if value.Root != "HKCU" {
				t.Fatal("not per-user")
			}
			commands[value.Key+"|"+value.Name] = value.Value
		}
		root := `Software\Classes\Directory\shell\FileES`
		for _, item := range []struct{ key, verb string }{{"01Add", "--explorer-add"}, {"02Detach", "--explorer-detach"}} {
			key := root + `\shell\` + item.key
			if commands[key+`\command|`] != `"[INSTALLFOLDER]filees-launch.exe" `+item.verb+` "%1"` {
				t.Fatal("unquoted or wrong gesture")
			}
			if commands[key+"|MultiSelectModel"] != "Single" {
				t.Fatal("not single folder")
			}
		}
		if len(component.Removes) != 1 || component.Removes[0].Root != "HKCU" || component.Removes[0].Key != root || component.Removes[0].Action != "removeOnUninstall" {
			t.Fatal("uninstall not scoped to our menu")
		}
		return
	}
	t.Fatal("missing classic menu")
}

func TestVirtualExplorerMenuIsConditionalAndNeverExecutesSVN(t *testing.T) {
	data, err := os.ReadFile("windows/filees.wxs")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, `<Component Id="FileESExplorerVirtualMenu"`)
	if start < 0 {
		t.Fatal("missing virtual folder menu")
	}
	end := strings.Index(source[start:], "</Component>")
	if end < 0 {
		t.Fatal("invalid component")
	}
	block := source[start : start+end]
	gate := `<?if $(var.WithCfapi) = "1" ?>`
	before := source[:start]
	if strings.LastIndex(before, gate) < strings.LastIndex(before, "<?endif?>") {
		t.Fatal("menu outside CFAPI gate")
	}
	ref := strings.Index(source, `<ComponentRef Id="FileESExplorerVirtualMenu"`)
	if ref < 0 {
		t.Fatal("component not installed")
	}
	before = source[:ref]
	if strings.LastIndex(before, gate) < strings.LastIndex(before, "<?endif?>") {
		t.Fatal("menu reference outside CFAPI gate")
	}
	for _, item := range []struct{ key, verb string }{{"03Download", "--explorer-download"}, {"04Release", "--explorer-release"}} {
		key := `Software\Classes\Directory\shell\FileES\shell\` + item.key
		for _, want := range []string{
			`Key="` + key + `" Name="MultiSelectModel" Type="string" Value="Single"`,
			`Key="` + key + `\command" Type="string" Value="&quot;[INSTALLFOLDER]filees-launch.exe&quot; ` + item.verb + ` &quot;%1&quot;"`,
			`Key="` + key + `" Action="removeOnUninstall"`,
		} {
			if !strings.Contains(block, want) {
				t.Fatalf("menu missing %s", want)
			}
		}
	}
	for _, unsafe := range []string{"svn.exe", "filees-cfapi.exe", "cmd.exe", "powershell", "runas", "Directory\\Background"} {
		if strings.Contains(block, unsafe) {
			t.Fatal("menu bypasses closed supervisor gesture", unsafe)
		}
	}
}
