import {
  Component,
  type ElementRef,
  Input,
  type OnChanges,
  type OnDestroy,
  type SimpleChanges,
  ViewChild,
} from "@angular/core";
import { FormsModule } from "@angular/forms";
import type { Unsubscribe } from "firebase/database";
import {
  FirebaseService,
  type HistoryMessage,
  type Instance,
  type StreamData,
} from "../../services/firebase.service";

interface Session {
  ID: string;
  Title: string;
}

interface DisplayMessage {
  role: "user" | "assistant";
  content: string;
  toolCalls: { name: string; status: string; detail: string }[];
}

@Component({
  selector: "app-prompt-panel",
  standalone: true,
  imports: [FormsModule],
  templateUrl: "./prompt-panel.component.html",
  styleUrl: "./prompt-panel.component.scss",
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
  loadingHistory = false;

  private unsubStream: Unsubscribe | null = null;

  constructor(private firebase: FirebaseService) {}

  private get uid(): string | null {
    return this.firebase.currentUser?.uid ?? null;
  }

  ngOnChanges(changes: SimpleChanges): void {
    if ("instance" in changes) {
      const prev = changes["instance"].previousValue as Instance | null;
      const curr = changes["instance"].currentValue as Instance | null;

      // Only reset state when the instance ID actually changes
      if (prev?.id !== curr?.id) {
        this.cleanup();
        this.sessions = [];
        this.selectedSessionId = "";
        this.responseText = "";
        this.streaming = false;
        this.toolCalls = [];
        this.history = [];
        if (this.instance) {
          this.loadSessions();
        }
      }
    }
  }

  ngOnDestroy(): void {
    this.cleanup();
  }

  async loadSessions() {
    if (!this.instance || !this.uid) return;
    try {
      const result = await this.firebase.sendCommandAndWait(
        this.uid,
        this.instance.id,
        "list_sessions",
      );
      this.sessions = (result as Session[]) ?? [];
      if (this.sessions.length > 0 && !this.selectedSessionId) {
        this.selectedSessionId = this.sessions[0].ID;
        this.loadHistory(this.selectedSessionId);
      }
    } catch {
      this.sessions = [];
    }
  }

  async createSession() {
    if (!this.instance || !this.uid) return;
    try {
      const result = await this.firebase.sendCommandAndWait(
        this.uid,
        this.instance.id,
        "create_session",
      );
      const session = result as Session;
      this.sessions = [...this.sessions, session];
      this.selectedSessionId = session.ID;
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
    if (this.selectedSessionId) {
      this.loadHistory(this.selectedSessionId);
    }
  }

  async loadHistory(sessionId: string) {
    if (!this.uid || !this.instance) return;
    this.loadingHistory = true;
    try {
      const messages = await this.firebase.getSessionHistory(this.uid, this.instance.id, sessionId);
      this.history = messages.map((m) => ({
        role: m.role,
        content: m.content,
        toolCalls: m.tool_calls || [],
      }));
    } catch {
      this.history = [];
    }
    this.loadingHistory = false;
    this.scrollToBottom();
  }

  async sendPrompt() {
    if (!this.instance || !this.selectedSessionId || !this.promptText.trim() || !this.uid) return;

    const content = this.promptText.trim();
    this.promptText = "";
    this.streaming = true;
    this.responseText = "";
    this.toolCalls = [];

    // Add user message to history immediately for responsive UI.
    this.history = [...this.history, { role: "user", content, toolCalls: [] }];
    this.scrollToBottom();

    // Clear stale stream data, then subscribe, then send command.
    await this.firebase.clearStream(this.uid, this.selectedSessionId);
    this.listenToStream(this.selectedSessionId);

    try {
      await this.firebase.sendCommand(this.uid, this.instance.id, "prompt", {
        session_id: this.selectedSessionId,
        content,
      });
    } catch {
      this.streaming = false;
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
    if (!this.uid) return;

    this.unsubStream = this.firebase.onStream(this.uid, sessionId, (data: StreamData | null) => {
      if (!data) return;

      this.responseText = data.content || "";
      this.toolCalls = data.tool_calls || [];

      if (data.status === "complete" || data.status === "error") {
        this.streaming = false;
        if (data.error) {
          this.responseText += `\n\n[ERROR] ${data.error}`;
        }
        // Move completed response into history.
        if (this.responseText) {
          this.history = [
            ...this.history,
            { role: "assistant", content: this.responseText, toolCalls: [...this.toolCalls] },
          ];
          this.responseText = "";
          this.toolCalls = [];
        }
        this.cleanup();
      }

      this.scrollToBottom();
    });
  }

  private cleanup() {
    this.unsubStream?.();
    this.unsubStream = null;
  }

  private scrollToBottom(): void {
    setTimeout(() => {
      const el = this.responseArea?.nativeElement;
      if (el) {
        el.scrollTop = el.scrollHeight;
      }
    });
  }
}
