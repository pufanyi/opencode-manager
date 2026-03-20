import { Component } from "@angular/core";
import { FormsModule } from "@angular/forms";
import { Router } from "@angular/router";
import { FirebaseService } from "../../services/firebase.service";

@Component({
  selector: "app-login",
  standalone: true,
  imports: [FormsModule],
  template: `
		<div class="login-container">
			<div class="login-card">
				<h1>OpenCode Manager</h1>
				<p class="subtitle">Sign in to your dashboard</p>

				@if (error) {
					<div class="error">{{ error }}</div>
				}

				<form (ngSubmit)="submit()">
					<input
						type="email"
						[(ngModel)]="email"
						name="email"
						placeholder="Email"
						autocomplete="email"
						required
					/>
					<input
						type="password"
						[(ngModel)]="password"
						name="password"
						placeholder="Password"
						autocomplete="current-password"
						required
					/>
					<button type="submit" [disabled]="loading">
						{{ loading ? 'Signing in...' : isRegister ? 'Create Account' : 'Sign In' }}
					</button>
				</form>

				<p class="toggle">
					{{ isRegister ? 'Already have an account?' : "Don't have an account?" }}
					<a (click)="isRegister = !isRegister">
						{{ isRegister ? 'Sign in' : 'Create one' }}
					</a>
				</p>
			</div>
		</div>
	`,
  styles: [
    `
		.login-container {
			display: flex;
			align-items: center;
			justify-content: center;
			min-height: 100vh;
			background: #0d1117;
		}

		.login-card {
			background: #161b22;
			border: 1px solid #30363d;
			border-radius: 12px;
			padding: 40px;
			width: 100%;
			max-width: 400px;

			h1 {
				color: #f0f6fc;
				margin: 0 0 4px;
				font-size: 24px;
			}

			.subtitle {
				color: #8b949e;
				margin: 0 0 24px;
				font-size: 14px;
			}
		}

		.error {
			background: rgba(248, 81, 73, 0.1);
			border: 1px solid #f85149;
			color: #f85149;
			padding: 10px 14px;
			border-radius: 6px;
			margin-bottom: 16px;
			font-size: 14px;
		}

		input {
			width: 100%;
			padding: 10px 14px;
			margin-bottom: 12px;
			background: #0d1117;
			border: 1px solid #30363d;
			border-radius: 6px;
			color: #f0f6fc;
			font-size: 14px;
			box-sizing: border-box;

			&:focus {
				outline: none;
				border-color: #58a6ff;
			}
		}

		button {
			width: 100%;
			padding: 10px;
			background: #238636;
			color: #fff;
			border: none;
			border-radius: 6px;
			font-size: 14px;
			font-weight: 600;
			cursor: pointer;

			&:hover:not(:disabled) { background: #2ea043; }
			&:disabled { opacity: 0.6; cursor: default; }
		}

		.toggle {
			text-align: center;
			color: #8b949e;
			margin-top: 16px;
			font-size: 13px;

			a {
				color: #58a6ff;
				cursor: pointer;
				&:hover { text-decoration: underline; }
			}
		}
	`,
  ],
})
export class LoginComponent {
  email = "";
  password = "";
  error = "";
  loading = false;
  isRegister = false;

  constructor(
    private firebase: FirebaseService,
    private router: Router,
  ) {}

  async submit() {
    this.error = "";
    this.loading = true;

    try {
      if (this.isRegister) {
        await this.firebase.register(this.email, this.password);
      } else {
        await this.firebase.login(this.email, this.password);
      }
      this.router.navigate(["/"]);
    } catch (e: unknown) {
      this.error = (e as Error).message || "Authentication failed";
    } finally {
      this.loading = false;
    }
  }
}
