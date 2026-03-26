import { Component, EventEmitter, Input, Output } from "@angular/core";
import { FormsModule } from "@angular/forms";
import { type Instance, ApiService } from "../../services/api.service";

@Component({
  selector: "app-instance-list",
  standalone: true,
  imports: [FormsModule],
  template: `
    <div class="instance-list">
      <div class="header">
        <h2>Instances</h2>
        <button class="btn btn-sm" (click)="showNewForm = !showNewForm">
          {{ showNewForm ? 'Cancel' : '+ New' }}
        </button>
      </div>

      @if (showNewForm) {
        <div class="new-form">
          <input [(ngModel)]="newName" placeholder="Name" />
          <input [(ngModel)]="newDirectory" placeholder="Directory" />
          <select [(ngModel)]="newProvider">
            <option value="claudecode">Claude Code</option>
            <option value="opencode">OpenCode</option>
          </select>
          <button class="btn" (click)="create()" [disabled]="!newName.trim() || !newDirectory.trim()">Create</button>
        </div>
      }

      <div class="list">
        @for (inst of instances; track inst.id) {
          <div class="instance-card" [class.selected]="selected?.id === inst.id" (click)="select.emit(inst)">
            <div class="card-header">
              <span class="name">{{ inst.name }}</span>
              <span class="status" [class]="'status-' + inst.status">{{ inst.status }}</span>
            </div>
            <div class="card-body">
              <span class="directory">{{ inst.directory }}</span>
              <span class="provider">{{ inst.provider_type }}</span>
            </div>
            <div class="card-actions">
              @if (inst.status === 'stopped') {
                <button class="btn btn-sm" (click)="start(inst, $event)">Start</button>
              }
              @if (inst.status === 'running') {
                <button class="btn btn-sm" (click)="stop(inst, $event)">Stop</button>
              }
              <button class="btn btn-sm btn-danger" (click)="remove(inst, $event)">Delete</button>
            </div>
          </div>
        } @empty {
          <p class="empty">No instances yet.</p>
        }
      </div>
    </div>
  `,
  styles: [`
    .instance-list { display: flex; flex-direction: column; gap: 8px; }
    .header { display: flex; justify-content: space-between; align-items: center; }
    .header h2 { margin: 0; font-size: 16px; }
    .new-form { display: flex; flex-direction: column; gap: 6px; padding: 8px; background: var(--bg-secondary); border-radius: 6px; }
    .new-form input, .new-form select { padding: 6px 8px; border: 1px solid var(--border); border-radius: 4px; background: var(--bg); color: var(--fg); }
    .list { display: flex; flex-direction: column; gap: 4px; }
    .instance-card { padding: 10px; border: 1px solid var(--border); border-radius: 6px; cursor: pointer; transition: background 0.1s; }
    .instance-card:hover { background: var(--bg-secondary); }
    .instance-card.selected { border-color: var(--accent); background: var(--bg-secondary); }
    .card-header { display: flex; justify-content: space-between; align-items: center; }
    .name { font-weight: 600; }
    .status { font-size: 12px; padding: 2px 6px; border-radius: 4px; }
    .status-running { color: #22c55e; }
    .status-stopped { color: #94a3b8; }
    .status-starting { color: #eab308; }
    .card-body { font-size: 12px; color: var(--fg-muted); margin-top: 4px; display: flex; justify-content: space-between; }
    .card-actions { margin-top: 6px; display: flex; gap: 4px; }
    .empty { color: var(--fg-muted); text-align: center; padding: 20px; }
  `],
})
export class InstanceListComponent {
  @Input() instances: Instance[] = [];
  @Input() selected: Instance | null = null;
  @Output() select = new EventEmitter<Instance>();
  @Output() changed = new EventEmitter<void>();

  showNewForm = false;
  newName = "";
  newDirectory = "";
  newProvider = "claudecode";

  constructor(private api: ApiService) {}

  async create() {
    if (!this.newName.trim() || !this.newDirectory.trim()) return;
    await this.api.createInstance(this.newName.trim(), this.newDirectory.trim(), this.newProvider);
    this.showNewForm = false;
    this.newName = "";
    this.newDirectory = "";
    this.changed.emit();
  }

  async start(inst: Instance, e: Event) {
    e.stopPropagation();
    await this.api.startInstance(inst.id);
    this.changed.emit();
  }

  async stop(inst: Instance, e: Event) {
    e.stopPropagation();
    await this.api.stopInstance(inst.id);
    this.changed.emit();
  }

  async remove(inst: Instance, e: Event) {
    e.stopPropagation();
    if (!confirm(`Delete "${inst.name}"?`)) return;
    await this.api.deleteInstance(inst.id);
    this.changed.emit();
  }
}
