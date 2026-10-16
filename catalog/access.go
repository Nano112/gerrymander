package catalog

type AccessEntry struct {
	Application  Application  `json:"application"`
	Service      Service      `json:"service"`
	Deployment   Deployment   `json:"deployment"`
	Capabilities Capabilities `json:"capabilities"`
}

// Access returns explicitly enabled definitions for one environment/workspace.
// No definition is a grant; callers still make their own authorization decisions.
func (s Snapshot) Access(environment, workspace string) []AccessEntry {
	apps := map[string]Application{}
	services := map[string]Service{}
	caps := map[string]Capabilities{}
	for _, a := range s.Applications {
		apps[a.ID] = a
	}
	for _, v := range s.Services {
		services[v.ID] = v
	}
	for _, c := range s.Capabilities {
		caps[c.ServiceID+"/"+c.Version] = c
	}
	result := []AccessEntry{}
	for _, d := range s.Deployments {
		if d.Environment != environment || d.Workspace != workspace || d.Lifecycle != "registered" || d.CapabilityVersion == "" {
			continue
		}
		a, appOK := apps[d.ApplicationID]
		v, serviceOK := services[d.ServiceID]
		c, capOK := caps[d.ServiceID+"/"+d.CapabilityVersion]
		if !appOK || !serviceOK || !capOK {
			continue
		}
		v.Display = InheritDisplay(v.Display, a.Display)
		result = append(result, AccessEntry{a, v, d, c})
	}
	return result
}

func InheritDisplay(display, parent Display) Display {
	if display.Name == "" {
		display.Name = parent.Name
	}
	if display.Description == "" {
		display.Description = parent.Description
	}
	if display.Logo == "" {
		display.Logo = parent.Logo
	}
	if display.Icon == "" {
		display.Icon = parent.Icon
	}
	if display.Color == "" {
		display.Color = parent.Color
	}
	links := map[string]string{}
	for k, v := range parent.Links {
		links[k] = v
	}
	for k, v := range display.Links {
		links[k] = v
	}
	display.Links = links
	return display
}
