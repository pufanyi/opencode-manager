import { inject } from "@angular/core";
import { type CanActivateFn, Router } from "@angular/router";
import { filter, map, take } from "rxjs";
import { FirebaseService } from "../services/firebase.service";

export const authGuard: CanActivateFn = () => {
  const firebase = inject(FirebaseService);
  const router = inject(Router);

  return firebase.user$.pipe(
    filter((user) => user !== null), // skip null = "still loading"
    take(1),
    map((user) => {
      if (user) return true; // User object = authenticated
      return router.createUrlTree(["/login"]); // false = checked, no user
    }),
  );
};
