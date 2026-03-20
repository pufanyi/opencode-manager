import { inject } from '@angular/core';
import { type CanActivateFn, Router } from '@angular/router';
import { FirebaseService } from '../services/firebase.service';
import { map, filter, take } from 'rxjs';

export const authGuard: CanActivateFn = () => {
	const firebase = inject(FirebaseService);
	const router = inject(Router);

	return firebase.user$.pipe(
		filter((user) => user !== undefined),
		take(1),
		map((user) => {
			if (user) return true;
			return router.createUrlTree(['/login']);
		}),
	);
};
