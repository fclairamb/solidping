import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

// Focus ring (every variant): a 2px --ring ring pushed 2px OUT from the edge
// by a --background-colored offset, so it stays visible on the solid fills.
//
// Children are spaced by gap-2. Many call sites still give a leading icon its
// own mr-2 (the old pattern); [&>svg]:mr-0 cancels it so the gap is not
// doubled.
//
// A hairline shadow at most, and hover changes the color only, never the
// position. The default variant is the one gradient button: the tonal
// --primary-gradient over the solid --primary-fill, with a white label; hover
// slides the gradient to its darker half (a background-position transition,
// the button itself does not move). Every other variant is flat.
//
// Brand variant: the solid crimson of the logo, for the primary action of the
// auth pages (login, register…), whose card already carries the crimson top
// border. Anywhere else, the primary action stays on the default variant.
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0 [&>svg]:mr-0",
  {
    variants: {
      variant: {
        default:
          "bg-primary-fill bg-primary-gradient text-primary-fill-foreground shadow-xs transition-[color,background-position] duration-300 hover:bg-bottom-right",
        brand:
          "bg-brand text-brand-foreground shadow-xs hover:bg-brand/90",
        destructive:
          "bg-destructive text-destructive-foreground shadow-xs hover:bg-destructive/90 dark:bg-destructive/80 dark:hover:bg-destructive/70",
        outline:
          "border border-input bg-control shadow-xs hover:bg-accent hover:text-accent-foreground",
        secondary:
          "bg-secondary text-secondary-foreground hover:bg-secondary/80",
        ghost: "hover:bg-accent hover:text-accent-foreground",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        default: "h-9 px-4 py-2",
        sm: "h-8 rounded-md px-3 text-xs",
        lg: "h-10 rounded-md px-8",
        icon: "h-9 w-9",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";
    return (
      <Comp
        className={cn(buttonVariants({ variant, size, className }))}
        ref={ref}
        {...props}
      />
    );
  }
);
Button.displayName = "Button";

export { Button, buttonVariants };
