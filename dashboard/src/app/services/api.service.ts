import { Injectable, NgZone } from "@angular/core";

export interface Instance {
  id: string;
  name: string;
  directory: string;
  status: string;
  provider_type: string;
  port?: number;
}

export interface Session {
  ID: string;
  Title: string;
}

export interface StreamEvent {
  session_id: string;
  event: {
    type: string;
    text?: string;
    toolName?: string;
    toolState?: string;
    toolDetail?: string;
    done?: boolean;
    error?: string;
  };
}

export interface Settings {
  web: boolean;
  telegram_configured: boolean;
  telegram_connected: boolean;
}

@Injectable({ providedIn: "root" })
export class ApiService {
  private baseUrl = "";

  constructor(private zone: NgZone) {}

  // -- Instances --

  async getInstances(): Promise<Instance[]> {
    const res = await fetch(`${this.baseUrl}/api/instances`);
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }

  async createInstance(name: string, directory: string, provider: string): Promise<Instance> {
    const res = await fetch(`${this.baseUrl}/api/instances`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, directory, provider }),
    });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }

  async startInstance(id: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/api/instances/${id}/start`, { method: "POST" });
    if (!res.ok) throw new Error(await res.text());
  }

  async stopInstance(id: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/api/instances/${id}/stop`, { method: "POST" });
    if (!res.ok) throw new Error(await res.text());
  }

  async deleteInstance(id: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/api/instances/${id}/delete`, { method: "POST" });
    if (!res.ok) throw new Error(await res.text());
  }

  // -- Sessions --

  async getSessions(instanceId: string): Promise<Session[]> {
    const res = await fetch(`${this.baseUrl}/api/instances/${instanceId}/sessions`);
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }

  async createSession(instanceId: string): Promise<Session> {
    const res = await fetch(`${this.baseUrl}/api/sessions/${instanceId}/new`, { method: "POST" });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }

  // -- Prompt --

  async sendPrompt(instanceId: string, sessionId: string, content: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/api/prompt`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ instance_id: instanceId, session_id: sessionId, content }),
    });
    if (!res.ok) throw new Error(await res.text());
  }

  async abort(instanceId: string, sessionId: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/api/abort`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ instance_id: instanceId, session_id: sessionId }),
    });
    if (!res.ok) throw new Error(await res.text());
  }

  // -- SSE Stream --

  connectStream(sessionId: string, onEvent: (evt: StreamEvent) => void): () => void {
    const url = `${this.baseUrl}/api/ws?session=${encodeURIComponent(sessionId)}`;
    const source = new EventSource(url);

    source.onmessage = (msg) => {
      try {
        const data: StreamEvent = JSON.parse(msg.data);
        this.zone.run(() => onEvent(data));
      } catch {
        // ignore parse errors
      }
    };

    source.onerror = () => {
      // EventSource auto-reconnects
    };

    return () => source.close();
  }

  // -- Settings --

  async getSettings(): Promise<Settings> {
    const res = await fetch(`${this.baseUrl}/api/settings`);
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }
}
