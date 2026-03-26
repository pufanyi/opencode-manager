import { Component, type ElementRef, Input, type OnChanges, type OnDestroy, type SimpleChanges, ViewChild } from "@angular/core";
import { FormsModule } from "@angular/forms";
import { type Instance, type Session, type StreamEvent, ApiService } from "../../services/api.service";

interface DisplayMessage {
  role: "user" | "assistant";
  content: string;
  toolCalls: { name: string; status: string; detail: string }[];
}

@Component({
  selector: "app-prompt-panel",
  standalone: true,
  imports: [FormsModule],
  template: `
    <div class="prompt-panel">
      @if (!instance) {
        <div class="empty">Select an instance to start.</div>
      } @else {
        <div class="toolbar">
          <select [(ngModel)]="selectedSessionId" (ngModelChange)="onSessionChange()">
            @for (s of sessions; track s.ID) {
              <option [value]="s.ID">{{ s.Title || s.ID }}</option>
            }
          </select>
          <button class="btn btn-sm" (click)="createSession()">+ Session</button>
          <button class="btn btn-sm" (click)="loadSessions()">Refresh</button>
        </div>

        <div class="messages" #responseArea>
          @for (msg of history; track $index) {
            <div class="message" [class]="'msg-' + msg.role">
              <div class="msg-role">{{ msg.role }}</div>
              <div class="msg-content">{{ msg.content }}</div>
              @for (tc of msg.toolCalls; track $index) {
                <div class="tool-call">
                  <span class="tool-name">{{ tc.name }}</span>
                  <span class="tool-status" [class]="'ts-' + tc.status">{{ tc.status }}</span>
                  @if (tc.detail) { <span class="tool-detail">{{ tc.detail }}</span> }
                </div>
              }
            </div>
          }

          @if (streaming || responseText) {
            <div class="message msg-assistant streaming">
              <div class="msg-role">assistant</div>
              <div class="msg-content">{{ responseText }}@if (streaming) {<span class="cursor">|</span>}</div>
              @for (tc of toolCalls; track $index) {
                <div class="tool-call">
                  <span class="tool-name">{{ tc.name }}</span>
                  <span class="tool-status" [class]="'ts-' + tc.status">{{ tc.status }}</span>
                  @if (tc.detail) { <span class="tool-detail">{{ tc.detail }}</span> }
                </div>
              }
            </div>
          }
        </div>

        <div class="input-area">
          <textarea
            [(ngModel)]="promptText"
            (keydown)="onKeyDown($event)"
            placeholder="Type a message..."
            [disabled]="streaming"
            rows="3"
          ></textarea>
          <div class="input-actions">
            @if (streaming) {
              <button class="btn btn-danger" (click)="abortPrompt()">Abort</button>
            } @else {
              <button class="btn" (click)="sendPrompt()" [disabled]="!promptText.trim() || !selectedSessionId">Send</button>
            }
          </div>
        </div>
      }
    </div>
  `,
  styles: [`
    .prompt-panel { display: flex; flex-direction: column; height: 100%; }
    .empty { color: var(--fg-muted); text-align: center; padding: 40px; }
    .toolbar { display: flex; gap: 6px; padding: 8px 0; align-items: center; }
    .toolbar select { flex: 1; padding: 6px 8px; border: 1px solid var(--border); border-radius: 4px; background: var(--bg); color: var(--fg); }
    .messages { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: 8px; padding: 8px 0; }
    .message { padding: 10px; border-radius: 6px; }
    .msg-user { background: var(--bg-secondary); }
    .msg-assistant { background: var(--bg); border: 1px solid var(--border); }
    .msg-role { font-size: 11px; font-weight: 600; text-transform: uppercase; color: var(--fg-muted); margin-bottom: 4px; }
    .msg-content { white-space: pre-wrap; word-break: break-word; font-family: 'Geist Mono', monospace; font-size: 13px; line-height: 1.5; }
    .cursor { animation: blink 1s step-end infinite; }
    @keyframes blink { 50% { opacity: 0; } }
    .tool-call { display: flex; gap: 6px; align-items: center; font-size: 12px; margin-top: 4px; padding: 4px 6px; background: var(--bg-secondary); border-radius: 4px; }
    .tool-name { font-weight: 600; }
    .ts-running { color: #eab308; }
    .ts-completed { color: #22c55e; }
    .ts-error { color: #ef4444; }
    .tool-detail { color: var(--fg-muted); flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .input-area { display: flex; gap: 8px; padding-top: 8px; border-top: 1px solid var(--border); }
    .input-area textarea { flex: 1; padding: 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--bg); color: var(--fg); font-family: inherit; font-size: 13px; resize: none; }
    .input-actions { display: flex; align-items: flex-end; }
  `],
})
export class PromptPanelComponent implements OnChanges, OnDestroy {
  @Input() instance: Instance | null = null;
  @ViewChild("responseArea") responseArea!: ElementRef<HTMLDivElement>;

  sessions: Session[] = [];
  selectedSessionId = "";
  promptText = "";
  responseText = "";
  streaming = false;
  toolCalls: { name: string; status: string; detail: string }[] = [];
  history: DisplayMessage[] = [];

  private closeStream: (() => void) | null = null;

  constructor(private api: ApiService) {}

  ngOnChanges(changes: SimpleChanges): void {
    if ("instance" in changes) {
      const prev = changes["instance"].previousValue as Instance | null;
      const curr = changes["instance"].currentValue as Instance | null;
      if (prev?.id !== curr?.id) {
        this.cleanup();
        this.sessions = [];
        this.selectedSessionId = "";
        this.responseText = "";
        this.streaming = false;
        this.toolCalls = [];
        this.history = [];
        if (this.instance) this.loadSessions();
      }
    }
  }

  ngOnDestroy(): void {
    this.cleanup();
  }

  async loadSessions() {
    if (!this.instance) return;
    try {
      this.sessions = await this.api.getSessions(this.instance.id);
      if (this.sessions.length > 0 && !this.selectedSessionId) {
        this.selectedSessionId = this.sessions[0].ID;
      }
    } catch {
      this.sessions = [];
    }
  }

  async createSession() {
    if (!this.instance) return;
    try {
      const session = await this.api.createSession(this.instance.id);
      this.sessions = [...this.sessions, session];
      this.selectedSessionId = session.ID;
      this.history = [];
      this.responseText = "";
      this.toolCalls = [];
    } catch (e) {
      console.error("Create session failed:", e);
    }
  }

  onSessionChange(): void {
    this.responseText = "";
    this.toolCalls = [];
    this.history = [];
    this.cleanup();
  }

  async sendPrompt() {
    if (!this.instance || !this.selectedSessionId || !this.promptText.trim()) return;

    const content = this.promptText.trim();
    this.promptText = "";
    this.streaming = true;
    this.responseText = "";
    this.toolCalls = [];

    this.history = [...this.history, { role: "user", content, toolCalls: [] }];
    this.scrollToBottom();

    // Connect SSE BEFORE sending prompt for zero-delay streaming.
    this.listenToStream(this.selectedSessionId);

    try {
      await this.api.sendPrompt(this.instance.id, this.selectedSessionId, content);
    } catch {
      this.streaming = false;
    }
  }

  async abortPrompt() {
    if (!this.instance || !this.selectedSessionId) return;
    try {
      await this.api.abort(this.instance.id, this.selectedSessionId);
    } catch {
      // ignore
    }
  }

  onKeyDown(event: KeyboardEvent): void {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      this.sendPrompt();
    }
  }

  private listenToStream(sessionId: string) {
    this.cleanup();

    this.closeStream = this.api.connectStream(sessionId, (data: StreamEvent) => {
      const evt = data.event;

      switch (evt.type) {
        case "text":
          this.responseText = evt.text || "";
          break;
        case "tool_use":
          this.updateToolCall(evt.toolName || "", evt.toolState || "", evt.toolDetail || "");
          break;
        case "done":
          this.finishStream();
          break;
        case "error":
          this.responseText += `\n\n[ERROR] ${evt.error || "Unknown error"}`;
          this.finishStream();
          break;
      }

      this.scrollToBottom();
    });
  }

  private updateToolCall(name: string, status: string, detail: string) {
    const existing = this.toolCalls.find((tc) => tc.name === name && tc.status !== "completed" && tc.status !== "error");
    if (existing) {
      existing.status = status;
      existing.detail = detail;
    } else {
      this.toolCalls = [...this.toolCalls, { name, status, detail }];
    }
  }

  private finishStream() {
    this.streaming = false;
    if (this.responseText || this.toolCalls.length) {
      this.history = [
        ...this.history,
        { role: "assistant", content: this.responseText, toolCalls: [...this.toolCalls] },
      ];
      this.responseText = "";
      this.toolCalls = [];
    }
    this.cleanup();
  }

  private cleanup() {
    this.closeStream?.();
    this.closeStream = null;
  }

  private scrollToBottom(): void {
    setTimeout(() => {
      const el = this.responseArea?.nativeElement;
      if (el) el.scrollTop = el.scrollHeight;
    });
  }
}
