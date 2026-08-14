export namespace core {
	
	export class PortAllocation {
	    id: number;
	    pool_id: number;
	    pool?: string;
	    project?: string;
	    value: number;
	    owner_ref: string;
	    state: string;
	    // Go type: time
	    last_verified_at: any;
	
	    static createFrom(source: any = {}) {
	        return new PortAllocation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.pool_id = source["pool_id"];
	        this.pool = source["pool"];
	        this.project = source["project"];
	        this.value = source["value"];
	        this.owner_ref = source["owner_ref"];
	        this.state = source["state"];
	        this.last_verified_at = this.convertValues(source["last_verified_at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class ClaimInput {
	    zone: string;
	    label: string;
	    kind: string;
	    wildcard: boolean;
	    backend: string;
	    listen: number[];
	    owner: string;
	
	    static createFrom(source: any = {}) {
	        return new ClaimInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.zone = source["zone"];
	        this.label = source["label"];
	        this.kind = source["kind"];
	        this.wildcard = source["wildcard"];
	        this.backend = source["backend"];
	        this.listen = source["listen"];
	        this.owner = source["owner"];
	    }
	}
	export class Listener {
	    port: number;
	    pid: number;
	    command: string;
	    addr: string;
	    user: string;
	    gerry_owner?: string;
	
	    static createFrom(source: any = {}) {
	        return new Listener(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.port = source["port"];
	        this.pid = source["pid"];
	        this.command = source["command"];
	        this.addr = source["addr"];
	        this.user = source["user"];
	        this.gerry_owner = source["gerry_owner"];
	    }
	}
	export class PortsView {
	    grants: core.PortAllocation[];
	    listeners: Listener[];
	
	    static createFrom(source: any = {}) {
	        return new PortsView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.grants = this.convertValues(source["grants"], core.PortAllocation);
	        this.listeners = this.convertValues(source["listeners"], Listener);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Settings {
	    api: string;
	    api_key: string;
	    compose_dir: string;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.api = source["api"];
	        this.api_key = source["api_key"];
	        this.compose_dir = source["compose_dir"];
	    }
	}
	export class Status {
	    api_reachable: boolean;
	    api_base: string;
	    zones: number;
	    allocations: number;
	    conflicts: number;
	    daemon_error?: string;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.api_reachable = source["api_reachable"];
	        this.api_base = source["api_base"];
	        this.zones = source["zones"];
	        this.allocations = source["allocations"];
	        this.conflicts = source["conflicts"];
	        this.daemon_error = source["daemon_error"];
	    }
	}
	export class TreeNode {
	    id?: number;
	    label: string;
	    fqdn: string;
	    kind?: string;
	    state?: string;
	    source?: string;
	    owner?: string;
	    project?: string;
	    wildcard?: boolean;
	    routes?: string[];
	    children?: TreeNode[];
	
	    static createFrom(source: any = {}) {
	        return new TreeNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.fqdn = source["fqdn"];
	        this.kind = source["kind"];
	        this.state = source["state"];
	        this.source = source["source"];
	        this.owner = source["owner"];
	        this.project = source["project"];
	        this.wildcard = source["wildcard"];
	        this.routes = source["routes"];
	        this.children = this.convertValues(source["children"], TreeNode);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

