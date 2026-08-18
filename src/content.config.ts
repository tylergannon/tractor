import { defineCollection } from "astro:content";
import { glob } from "astro/loaders";
import { z } from "astro/zod";

const docs = defineCollection({
  loader: glob({ pattern: "**/*.{md,mdx}", base: "./src/content/docs" }),
  schema: z.object({
    title: z.string(),
    description: z.string(),
    eyebrow: z.string(),
    order: z.number().int().positive(),
    sourceLabel: z.string().optional(),
    sourceUrl: z.url().optional(),
  }),
});

export const collections = { docs };
