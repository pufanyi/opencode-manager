import {
  Component,
  type ElementRef,
  Input,
  inject,
  NgZone,
  type OnChanges,
  type OnDestroy,
  type SimpleChanges,
  ViewChild,
} from "@angular/core";
import { FormsModule } from "@angular/forms";
import {
  ApiService,
  type Instance,
  type Session,
  type StreamEvent,
} from "../../services/api.service";

@Component({
  selector: "app-prompt-panel",
  imports: [FormsModule],
  templateUrl: "./prompt-panel.component.html",
  styleUrl: "./prompt-panel.component.scss",
})
export class PromptPanelComponent implements OnChanges, OnDestroy {
  private api = inject(ApiService);
  private zone = inject(NgZone);

  @Input() instance: Instance | null = null;
  @ViewChild("responseArea") responseArea!: ElementRef<HTMLPreElement>;

  sessions: Session[] = [];
  selectedSessionId = "";
  promptText = "";
  responseText = "";
  streaming = false;

  private eventSource: EventSource | null = null;

  ngOnChanges(changes: SimpleChanges): void {
    if (changes["instance"]) {
      this.closeSSE();
      this.sessions = [];
      this.selectedSessionId = "";
      this.responseText = "";
      this.streaming = false;
      if (this.instance) {
        this.loadSessions();
      }
    }
  }

  ngOnDestroy(): void {
    this.closeSSE();
  }

  loadSessions(): void {
    if (!this.instance) return;
    this.api.getSessions(this.instance.id).subscribe({
      next: (sessions) => {
        this.sessions = sessions ?? [];
        if (this.sessions.length > 0 && !this.selectedSessionId) {
          this.selectedSessionId = this.sessions[0].ID;
          this.connectSSE();
        }
      },
      error: () => {
        this.sessions = [];
      },
    });
  }

  createSession(): void {
    if (!this.instance) return;
    this.api.createSession(this.instance.id).subscribe({
      next: (session) => {
        this.sessions = [...this.sessions, session];
        this.selectedSessionId = session.ID;
        this.responseText = "";
        this.connectSSE();
      },
    });
  }

  onSessionChange(): void {
    this.responseText = "";
    this.connectSSE();
  }

  sendPrompt(): void {
    if (!this.instance || !this.selectedSessionId || !this.promptText.trim()) return;
    this.streaming = true;
    this.responseText = "";
    this.api
      .sendPrompt({
        instance_id: this.instance.id,
        session_id: this.selectedSessionId,
        content: this.promptText.trim(),
      })
      .subscribe({
        error: () => {
          this.streaming = false;
        },
      });
    this.promptText = "";
  }

  abort(): void {
    if (!this.instance || !this.selectedSessionId) return;
    this.api
      .abortPrompt({
        instance_id: this.instance.id,
        session_id: this.selectedSessionId,
      })
      .subscribe({
        next: () => {
          this.streaming = false;
        },
      });
  }

  onKeyDown(event: KeyboardEvent): void {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      this.sendPrompt();
    }
  }

  private connectSSE(): void {
    this.closeSSE();
    if (!this.selectedSessionId) return;

    const es = this.api.connectSSE(this.selectedSessionId);

    es.onmessage = (msg: MessageEvent) => {
      this.zone.run(() => {
        try {
          const data: StreamEvent = JSON.parse(msg.data);
          const evt = data.event;

          if (evt.Error) {
            this.responseText += `\n[ERROR] ${evt.Error}\n`;
            this.streaming = false;
          } else if (evt.Text) {
            this.responseText = evt.Text;
          } else if (evt.ToolName) {
            const state = evt.ToolState ?? "";
            this.responseText += `\n[Tool: ${evt.ToolName}] ${state}\n`;
          }

          if (evt.Done) {
            this.streaming = false;
          }

          this.scrollToBottom();
        } catch {
          // ignore non-JSON lines
        }
      });
    };

    es.onerror = () => {
      this.zone.run(() => {
        this.streaming = false;
      });
    };

    this.eventSource = es;
  }

  private closeSSE(): void {
    if (this.eventSource) {
      this.eventSource.close();
      this.eventSource = null;
    }
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
