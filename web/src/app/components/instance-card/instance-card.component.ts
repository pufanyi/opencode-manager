import { Component, EventEmitter, Input, Output } from '@angular/core';
import { Instance } from '../../services/api.service';

@Component({
  selector: 'app-instance-card',
  imports: [],
  templateUrl: './instance-card.component.html',
  styleUrl: './instance-card.component.scss',
})
export class InstanceCardComponent {
  @Input({ required: true }) instance!: Instance;
  @Output() selectInstance = new EventEmitter<Instance>();
  @Output() startInstance = new EventEmitter<Instance>();
  @Output() stopInstance = new EventEmitter<Instance>();
  @Output() deleteInstance = new EventEmitter<Instance>();

  get statusColor(): string {
    switch (this.instance.status) {
      case 'running':
        return 'var(--accent-green)';
      case 'stopped':
        return 'var(--accent-red)';
      default:
        return 'var(--accent-yellow)';
    }
  }

  get providerBadge(): string {
    return this.instance.provider_type === 'claudecode' ? 'CC' : 'OC';
  }

  get isRunning(): boolean {
    return this.instance.status === 'running';
  }
}
