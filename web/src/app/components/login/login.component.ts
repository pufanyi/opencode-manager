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

				<button class="google-btn" (click)="loginWithGoogle()" [disabled]="loading">
					<svg viewBox="0 0 24 24" width="18" height="18">
						<path fill="#4285F4" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z"/>
						<path fill="#34A853" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"/>
						<path fill="#FBBC05" d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z"/>
						<path fill="#EA4335" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z"/>
					</svg>
					Sign in with Google
				</button>

				<div class="divider"><span>or</span></div>

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

		.google-btn {
			width: 100%;
			padding: 10px;
			background: #fff;
			color: #3c4043;
			border: 1px solid #dadce0;
			border-radius: 6px;
			font-size: 14px;
			font-weight: 500;
			cursor: pointer;
			display: flex;
			align-items: center;
			justify-content: center;
			gap: 10px;

			&:hover:not(:disabled) { background: #f7f8f8; }
			&:disabled { opacity: 0.6; cursor: default; }
		}

		.divider {
			display: flex;
			align-items: center;
			margin: 16px 0;
			color: #484f58;
			font-size: 12px;

			&::before, &::after {
				content: '';
				flex: 1;
				border-bottom: 1px solid #30363d;
			}
			span { padding: 0 12px; }
		}

		button[type="submit"] {
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

  async loginWithGoogle() {
    this.error = "";
    this.loading = true;
    try {
      await this.firebase.loginWithGoogle();
      this.router.navigate(["/"]);
    } catch (e: unknown) {
      this.error = (e as Error).message || "Google sign-in failed";
    } finally {
      this.loading = false;
    }
  }

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
