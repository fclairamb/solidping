import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

// Focus ring (every variant): a 2px --ring ring pushed 2px OUT from the edge
// by a --background-colored offset. The gap is what keeps the ring visible on
// the gradient default button, whose own blue would swallow a ring drawn flush
// against it. Same recipe as the Switch.
//
// Default variant (electric identity, spec 2026-09-24-01): bg-primary stays
// as the background-color underneath bg-primary-gradient, so the button still
// reads blue if the gradient ever fails to paint. Labels sit only on
// --primary-gradient (>= 4.4:1 white at every stop), never on the brighter
// --accent-gradient. The hover lift is behind motion-safe:.
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg text-sm font-medium transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default:
          "bg-primary bg-primary-gradient text-gradient-foreground inset-shadow-highlight shadow-primary hover:brightness-105 hover:shadow-primary-hover motion-safe:hover:-translate-y-px",
        destructive:
          "bg-destructive text-white shadow-destructive hover:bg-destructive/90 hover:shadow-destructive-hover",
        outline:
          "border border-input bg-control shadow-sm hover:bg-accent hover:text-accent-foreground",
        secondary:
          "bg-secondary text-secondary-foreground shadow-sm hover:bg-secondary/80",
        ghost: "hover:bg-accent hover:text-accent-foreground",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        default: "h-9 px-4 py-2",
        sm: "h-8 rounded-lg px-3 text-xs",
        lg: "h-10 rounded-lg px-8",
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
